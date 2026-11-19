package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	releaseRepo   = "perorin0418/hashi"
	checksumsName = "checksums.txt"
)

type release struct {
	TagName string `json:"tag_name"`
	HTMLURL string `json:"html_url"`
	Assets  []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

// cmdSelfUpdate は GitHub の最新リリースから、この OS・CPU 向けの実行ファイルを取得して自分を入れ替える。
func cmdSelfUpdate(args []string) error {
	fs := flag.NewFlagSet("self-update", flag.ContinueOnError)
	check := fs.Bool("check", false, "新しい版があるかを表示するだけで、入れ替えない")
	force := fs.Bool("force", false, "同じ版でも入れ替える")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	cleanupOld()

	rel, err := latestRelease(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "今の版: %s / 最新の版: %s\n", version, rel.TagName)
	if !*force && !isNewer(rel.TagName, version) {
		fmt.Fprintln(os.Stderr, "最新の版です")
		return nil
	}
	if *check {
		fmt.Fprintf(os.Stderr, "新しい版があります: %s\n", rel.HTMLURL)
		return nil
	}

	name := assetName(runtime.GOOS, runtime.GOARCH)
	binURL, sumURL := "", ""
	for _, a := range rel.Assets {
		switch a.Name {
		case name:
			binURL = a.URL
		case checksumsName:
			sumURL = a.URL
		}
	}
	if binURL == "" {
		return fmt.Errorf("リリース %s に %s がありません（この OS・CPU 向けは配布していません）", rel.TagName, name)
	}
	if sumURL == "" {
		return fmt.Errorf("リリース %s に %s がありません", rel.TagName, checksumsName)
	}

	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return err
	}

	sums, err := download(ctx, sumURL)
	if err != nil {
		return err
	}
	want, err := findChecksum(sums, name)
	if err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "%s を取得しています\n", name)
	bin, err := download(ctx, binURL)
	if err != nil {
		return err
	}
	got := sha256.Sum256(bin)
	if hex.EncodeToString(got[:]) != want {
		return fmt.Errorf("チェックサムが一致しません（取得したファイルが壊れているか、改ざんされています）")
	}

	if err := replaceExecutable(exe, bin); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "%s に更新しました: %s\n", rel.TagName, exe)
	return nil
}

func latestRelease(ctx context.Context) (*release, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", releaseRepo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "hashi-self-update")
	// 未認証の API は 1 時間 60 回まで。足りなければ GITHUB_TOKEN を使う
	if t := os.Getenv("GITHUB_TOKEN"); t != "" {
		req.Header.Set("Authorization", "Bearer "+t)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("最新のリリースを取得できません: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return nil, errors.New("リリースがまだありません")
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("最新のリリースを取得できません: HTTP %d", res.StatusCode)
	}
	var r release
	if err := json.NewDecoder(res.Body).Decode(&r); err != nil {
		return nil, err
	}
	return &r, nil
}

func download(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "hashi-self-update")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ダウンロードできません: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ダウンロードできません: %s: HTTP %d", url, res.StatusCode)
	}
	return io.ReadAll(io.LimitReader(res.Body, 200<<20))
}

// findChecksum は sha256sum の出力形式（"<hex>  <ファイル名>"）から該当ファイルの値を探す。
func findChecksum(sums []byte, name string) (string, error) {
	sc := bufio.NewScanner(strings.NewReader(string(sums)))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name {
			return strings.ToLower(f[0]), nil
		}
	}
	return "", fmt.Errorf("%s に %s の値がありません", checksumsName, name)
}

func assetName(goos, goarch string) string {
	n := fmt.Sprintf("hashi-%s-%s", goos, goarch)
	if goos == "windows" {
		n += ".exe"
	}
	return n
}

// replaceExecutable は実行中のファイルを新しい中身に入れ替える。
// 同じフォルダに書いてから rename するので、途中で失敗しても元のファイルは壊れない。
// Windows は実行中のファイルを上書きできないが、名前は変えられるので、古いものを .old に退避する。
func replaceExecutable(exe string, bin []byte) error {
	tmp := exe + ".new"
	if err := os.WriteFile(tmp, bin, 0o755); err != nil {
		if errors.Is(err, os.ErrPermission) {
			return fmt.Errorf("%s に書き込めません。権限のあるユーザーで実行してください（例: sudo hashi self-update）", filepath.Dir(exe))
		}
		return err
	}
	if runtime.GOOS == "windows" {
		old := exe + ".old"
		_ = os.Remove(old)
		if err := os.Rename(exe, old); err != nil {
			os.Remove(tmp)
			return err
		}
		if err := os.Rename(tmp, exe); err != nil {
			_ = os.Rename(old, exe)
			return err
		}
		return nil
	}
	if err := os.Rename(tmp, exe); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// cleanupOld は前回の更新で Windows に残った .old を消す（実行中でなくなっていれば消せる）。
func cleanupOld() {
	if runtime.GOOS != "windows" {
		return
	}
	if exe, err := os.Executable(); err == nil {
		_ = os.Remove(exe + ".old")
	}
}

// isNewer は latest が current より新しい版かを返す。
// current が vX.Y.Z の形でない（手元でビルドした dev など）ときは、常に新しいとみなす。
func isNewer(latest, current string) bool {
	l, ok := parseVersion(latest)
	if !ok {
		return false
	}
	c, ok := parseVersion(current)
	if !ok {
		return true
	}
	for i := range l {
		if l[i] != c[i] {
			return l[i] > c[i]
		}
	}
	return false
}

func parseVersion(s string) ([3]int, bool) {
	var v [3]int
	s = strings.TrimPrefix(s, "v")
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return v, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return v, false
		}
		v[i] = n
	}
	return v, true
}
