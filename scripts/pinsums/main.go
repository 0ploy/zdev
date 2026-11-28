// Command pinsums checks or refreshes the SHA-256 checksums of the tools zdev
// downloads (config.ToolReleases in internal/config/defaults.go): it
// downloads every pinned platform's release and compares its checksum with
// the pin. Renovate bumps a tool's version but can't compute checksums, so
// it runs `go run ./scripts/pinsums -w` after the bump (postUpgradeTasks in
// renovate.json), and CI runs it without -w to check them.
//
// A checksum is only as trustworthy as the download it was computed from:
// reviewing a bump means trusting that release, as with any new version.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/0ploy/zdev/internal/config"
)

func main() {
	write := flag.Bool("w", false, "write changed checksums into the file instead of failing")
	file := flag.String("file", "internal/config/defaults.go", "the file holding the pins")
	flag.Parse()

	src, err := os.ReadFile(*file)
	if err != nil {
		fail(err)
	}
	text := string(src)
	client := &http.Client{Timeout: 10 * time.Minute}
	var stale []string
	for _, r := range config.ToolReleases {
		platforms := make([]string, 0, len(r.SHA256))
		for p := range r.SHA256 {
			platforms = append(platforms, p)
		}
		sort.Strings(platforms)
		for _, p := range platforms {
			goos, goarch, _ := strings.Cut(p, "/")
			url := r.DownloadURL(goos, goarch)
			sum, err := download(client, url)
			if err != nil {
				fail(fmt.Errorf("%s %s (%s): %w", r.Name, r.Version, p, err))
			}
			if sum == r.SHA256[p] {
				fmt.Printf("ok      %s %s %s\n", r.Name, r.Version, p)
				continue
			}
			fmt.Printf("changed %s %s %s: %s\n", r.Name, r.Version, p, sum)
			stale = append(stale, r.Name+" "+p)
			if text, err = replaceSum(text, r.Name, p, sum); err != nil {
				fail(err)
			}
		}
	}
	switch {
	case len(stale) == 0:
		return
	case *write:
		if err := os.WriteFile(*file, []byte(text), 0o644); err != nil {
			fail(err)
		}
		fmt.Printf("wrote %d checksums into %s\n", len(stale), *file)
	default:
		fail(fmt.Errorf("checksums don't match their pins (%s); run: go run ./scripts/pinsums -w", strings.Join(stale, ", ")))
	}
}

// download returns the SHA-256 of what url serves.
func download(client *http.Client, url string) (string, error) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	res, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s: %s", url, res.Status)
	}
	h := sha256.New()
	if _, err := io.Copy(h, io.LimitReader(res.Body, 1<<30)); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// replaceSum sets the checksum of platform in the ToolRelease named name:
// the `"<platform>": "<hex>"` line between `ToolRelease{Name: "<name>"` and
// the block's closing `}}`.
func replaceSum(text, name, platform, sum string) (string, error) {
	start := strings.Index(text, `ToolRelease{Name: "`+name+`"`)
	if start < 0 {
		return "", fmt.Errorf("no ToolRelease named %q", name)
	}
	end := strings.Index(text[start:], "}}")
	if end < 0 {
		return "", fmt.Errorf("ToolRelease %q has no closing }}", name)
	}
	end += start
	line := regexp.MustCompile(`("` + regexp.QuoteMeta(platform) + `":\s*")[0-9a-f]{64}(")`)
	block := text[start:end]
	if !line.MatchString(block) {
		return "", fmt.Errorf("ToolRelease %q has no checksum line for %s", name, platform)
	}
	block = line.ReplaceAllString(block, "${1}"+sum+"${2}")
	return text[:start] + block + text[end:], nil
}

func fail(err error) {
	if errors.Is(err, os.ErrNotExist) {
		err = fmt.Errorf("%w (run from the repository root)", err)
	}
	fmt.Fprintln(os.Stderr, "pinsums:", err)
	os.Exit(1)
}
