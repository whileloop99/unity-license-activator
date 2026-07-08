// Unity License Activation — automated via chromedp.
//
// Usage:
//
//	go run . --email <email> --password <pass> --alf <file> [flags]
//
// Flags:
//
//	--email      Unity ID email (required)
//	--password   Unity ID password (required)
//	--alf        Path to .alf license request file (required)
//	--totp       Base32 TOTP secret (required if 2FA is enabled)
//	--ulf-name   ULF output filename (default: unity.ulf)
//	--output     Output directory for ULF, screenshots, error dumps (default: .output)
//	--data-dir   Persistent Chrome profile dir — reuses session on subsequent
//	             runs, skipping login (~18s vs ~34s). Created automatically.
//
// Output directory (--output) contains:
//
//	{ulf-name}   Downloaded license file
//	state.png    Screenshot on failure
//	error.png    Screenshot on crash
//	error.html   Page HTML on crash
//
// Flow: navigate → [login → TFA if needed →] upload ALF → select license → download ULF.
// With --data-dir: subsequent runs skip login (session reuse ~18s total).
// Exits non-zero on error.
package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/chromedp"
	"github.com/go-rod/rod/lib/launcher"
)

// ── constants ────────────────────────────────────────────────────────────────

var (
	email     string
	password  string
	alfFile   string
	totpKey   string
	ulfName   string
	outputDir string
	dataDir   string
)

// ── timestamp ────────────────────────────────────────────────────────────────

func ts() string {
	d := time.Now()
	return fmt.Sprintf("%04d/%02d/%02d %02d:%02d:%02d",
		d.Year(), int(d.Month()), d.Day(), d.Hour(), d.Minute(), d.Second())
}

// ── sleep ────────────────────────────────────────────────────────────────────

func sleep(ms int) { time.Sleep(time.Duration(ms) * time.Millisecond) }

// ── TOTP (RFC 6238) — mirrors JS implementation exactly ──────────────────────

const b32Alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"

func b32decode(s string) ([]byte, error) {
	s = strings.ToUpper(strings.TrimRight(s, "="))
	bits := make([]int, 0, len(s)*5)
	for _, ch := range s {
		v := strings.IndexRune(b32Alphabet, ch)
		if v < 0 {
			return nil, fmt.Errorf("invalid base32 char: %c", ch)
		}
		for i := 4; i >= 0; i-- {
			bits = append(bits, (v>>i)&1)
		}
	}
	buf := make([]byte, len(bits)/8)
	for i := range buf {
		var b byte
		for j := 0; j < 8; j++ {
			b = (b << 1) | byte(bits[i*8+j])
		}
		buf[i] = b
	}
	return buf, nil
}

func totp(key string) (string, error) {
	k, err := b32decode(key)
	if err != nil {
		return "", fmt.Errorf("invalid base32 key: %w", err)
	}
	epoch := time.Now().Unix() / 30
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, uint64(epoch))
	mac := hmac.New(sha1.New, k)
	mac.Write(buf)
	h := mac.Sum(nil)
	o := h[19] & 0xf
	code := (int(h[o]&0x7f)<<24 | int(h[o+1])<<16 | int(h[o+2])<<8 | int(h[o+3])) % 1_000_000
	return fmt.Sprintf("%06d", code), nil
}

// ── browser helpers ──────────────────────────────────────────────────────────

// fill: wait for selector, select-all (≡ triple-click), then type value.
func fill(ctx context.Context, sel, val string) error {
	tCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return chromedp.Run(tCtx,
		chromedp.WaitVisible(sel, chromedp.ByQuery),
		chromedp.Evaluate(fmt.Sprintf(`document.querySelector(%q).select()`, sel), nil),
		chromedp.SendKeys(sel, val, chromedp.ByQuery),
	)
}

// elemExists checks whether a CSS selector matches anything in the DOM.
func elemExists(ctx context.Context, sel string) bool {
	var n int
	err := chromedp.Run(ctx, chromedp.Evaluate(fmt.Sprintf(`document.querySelectorAll(%q).length`, sel), &n))
	if err != nil {
		return false
	}
	return n > 0
}

// waitTimeout runs chromedp actions with a timeout.
func waitTimeout(ctx context.Context, timeout time.Duration, actions ...chromedp.Action) error {
	tCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return chromedp.Run(tCtx, actions...)
}

// ── main flow ────────────────────────────────────────────────────────────────
func run(ctx context.Context, cwd string) error {
	var currentURL string

	// ── Phase 1: Try session (skip login if profile has valid cookies) ────
	fmt.Printf("%s [INFO] Loading license page...\n", ts())
	chromedp.Run(ctx, chromedp.Navigate("https://license.unity3d.com/manual"))

	// Poll: either upload field appears (authenticated) or redirect to login
	var needsLogin bool
	for i := 0; i < 60; i++ {
		sleep(200)
		chromedp.Run(ctx, chromedp.Location(&currentURL))
		if strings.Contains(currentURL, "login.unity.com") {
			needsLogin = true
			break
		}
		if elemExists(ctx, `input[name="licenseFile"]`) {
			break
		}
	}

	// If neither detected, wait a bit more for upload field
	if !needsLogin && !elemExists(ctx, `input[name="licenseFile"]`) {
		chromedp.Run(ctx, chromedp.Location(&currentURL))
		needsLogin = strings.Contains(currentURL, "login.unity.com")
	}

	if needsLogin {
		fmt.Printf("%s [INFO] Session expired, logging in...\n", ts())

		waitTimeout(ctx, 20*time.Second, chromedp.WaitVisible(`input[name="email"]`, chromedp.ByQuery))

		// Accept cookies if the banner shows
		var cookieAccepted bool
		chromedp.Run(ctx, chromedp.Evaluate(`
			(function() {
				const btns = Array.from(document.querySelectorAll("button"));
				const a = btns.find(b => b.textContent.trim() === "Accept All Cookies");
				if (a) { a.click(); return true; }
				return false;
			})()
		`, &cookieAccepted))
		if cookieAccepted {
			fmt.Printf("%s [INFO] Cookies accepted\n", ts())
		}

		// Email
		fmt.Printf("%s [INFO] Filling email...\n", ts())
		if err := fill(ctx, `input[name="email"]`, email); err != nil {
			return fmt.Errorf("fill email: %w", err)
		}
		chromedp.Run(ctx, chromedp.Evaluate(`document.querySelector('button[type="submit"]').click()`, nil))

		// Wait for password field
		waitTimeout(ctx, 15*time.Second, chromedp.WaitVisible(`input[type="password"]`, chromedp.ByQuery))

		if elemExists(ctx, `input[type="password"]`) {
			fmt.Printf("%s [INFO] Filling password...\n", ts())
			if err := fill(ctx, `input[type="password"]`, password); err != nil {
				return fmt.Errorf("fill password: %w", err)
			}
			chromedp.Run(ctx, chromedp.Evaluate(`document.querySelector('button[type="submit"]').click()`, nil))
		}

		// Wait for navigation — TFA or license page
		for i := 0; i < 60; i++ {
			sleep(200)
			chromedp.Run(ctx, chromedp.Location(&currentURL))
			if strings.Contains(currentURL, "sign-in/tfa") || strings.Contains(currentURL, "license.unity3d.com") {
				break
			}
		}
		if !strings.Contains(currentURL, "sign-in/tfa") && !strings.Contains(currentURL, "license.unity3d.com") {
			return fmt.Errorf("login failed — stuck at %s", currentURL)
		}
		fmt.Printf("%s [1] %s\n", ts(), currentURL)

		// 2FA TOTP if prompted
		if strings.Contains(currentURL, "sign-in/tfa") {
			waitTimeout(ctx, 10*time.Second, chromedp.WaitVisible(`input[autocomplete="one-time-code"]`, chromedp.ByQuery))
			if totpKey == "" {
				return fmt.Errorf("2FA required but no --totp key provided")
			}
			code, err := totp(totpKey)
			if err != nil {
				return fmt.Errorf("totp: %w", err)
			}
			fmt.Printf("%s [INFO] 2FA TOTP code: %s\n", ts(), code[:3]+"***")
			tfaSel := `input[autocomplete="one-time-code"]`
			if !elemExists(ctx, tfaSel) {
				tfaSel = `input[name="conversations_tfa_required_form\[verify_code\]"]`
			}
			if !elemExists(ctx, tfaSel) {
				tfaSel = `input:not([type="hidden"]):not([type="checkbox"]):not([name="email"])`
			}
			if elemExists(ctx, tfaSel) {
				chromedp.Run(ctx,
					chromedp.Evaluate(fmt.Sprintf(`document.querySelector(%q).select()`, tfaSel), nil),
					chromedp.SendKeys(tfaSel, code, chromedp.ByQuery),
				)
				if elemExists(ctx, `button[type="submit"]`) {
					chromedp.Run(ctx, chromedp.Evaluate(`document.querySelector('button[type="submit"]').click()`, nil))
					for i := 0; i < 60; i++ {
						sleep(200)
						chromedp.Run(ctx, chromedp.Location(&currentURL))
						if strings.Contains(currentURL, "license.unity3d.com") {
							break
						}
					}
				}
			}
			if !strings.Contains(currentURL, "license.unity3d.com") {
				return fmt.Errorf("TFA redirect timeout — stuck at %s", currentURL)
			}
		}

		// Navigate to license page if not already there
		if !strings.Contains(currentURL, "license.unity3d.com") {
			chromedp.Run(ctx, chromedp.Navigate("https://license.unity3d.com/manual"))
		}
		waitTimeout(ctx, 15*time.Second, chromedp.WaitReady("body"))
		waitTimeout(ctx, 15*time.Second, chromedp.WaitVisible(`input[name="licenseFile"]`, chromedp.ByQuery))
		chromedp.Run(ctx, chromedp.Location(&currentURL))
	}

	chromedp.Run(ctx, chromedp.Location(&currentURL))
	fmt.Printf("%s [2 on license] %s\n", ts(), currentURL)

	// 6. ToS
	tosSel := `button[name="conversations_accept_updated_tos_form\[accept\]"]`
	if elemExists(ctx, tosSel) {
		fmt.Printf("%s [INFO] ToS accepted\n", ts())
		chromedp.Run(ctx, chromedp.Click(tosSel, chromedp.ByQuery))
		// ToS acceptance may navigate; wait for body to reload
		waitTimeout(ctx, 15*time.Second, chromedp.WaitReady("body"))
	}

	// 7. Upload ALF
	if !elemExists(ctx, `input[name="licenseFile"]`) {
		fmt.Printf("%s [WARN] No license upload field\n", ts())
		var buf []byte
		chromedp.Run(ctx, chromedp.FullScreenshot(&buf, 90))
		os.WriteFile(filepath.Join(outputDir, "state.png"), buf, 0644)
		var bodyText string
		chromedp.Run(ctx, chromedp.Evaluate(`document.body.textContent`, &bodyText))
		if len(bodyText) > 500 {
			bodyText = bodyText[:500]
		}
		chromedp.Run(ctx, chromedp.Location(&currentURL))
		fmt.Printf("%s Page (%s): %s\n", ts(), currentURL, bodyText)
		return nil
	}

	fmt.Printf("%s [INFO] Uploading ALF...\n", ts())
	alfPath := alfFile // already absolute (resolved in main)
	if err := chromedp.Run(ctx,
		chromedp.SetUploadFiles(`input[name="licenseFile"]`, []string{alfPath}, chromedp.ByQuery),
	); err != nil {
		return fmt.Errorf("upload ALF: %w", err)
	}
	if elemExists(ctx, `input[name="commit"]`) {
		chromedp.Run(ctx, chromedp.Click(`input[name="commit"]`, chromedp.ByQuery))
		// Wait for license type selection page
		waitTimeout(ctx, 15*time.Second, chromedp.WaitVisible(`input[id="type_personal"]`, chromedp.ByQuery))
	}

	// 8. Select personal license
	fmt.Printf("%s [INFO] Selecting license type...\n", ts())
	chromedp.Run(ctx, chromedp.Location(&currentURL))
	fmt.Printf("%s [DBG] pre-select URL: %s\n", ts(), currentURL)

	personalExists := elemExists(ctx, `input[id="type_personal"][value="personal"]`)
	fmt.Printf("%s [DBG] type_personal exists: %v\n", ts(), personalExists)
	if personalExists {
		chromedp.Run(ctx, chromedp.Evaluate(
			`document.querySelector('input[id="type_personal"][value="personal"]')?.click()`, nil,
		))
	} else {
		// Dump visible radio inputs to see what selectors are present
		var radios string
		chromedp.Run(ctx, chromedp.Evaluate(
			`Array.from(document.querySelectorAll('input[type="radio"]')).map(e=>e.id+'/'+e.name+'/'+e.value).join(', ')`, &radios,
		))
		fmt.Printf("%s [DBG] radios on page: %s\n", ts(), radios)
	}
	sleep(500)

	option3Exists := elemExists(ctx, `input[id="option3"][name="personal_capacity"]`)
	fmt.Printf("%s [DBG] option3 exists: %v\n", ts(), option3Exists)
	if option3Exists {
		chromedp.Run(ctx, chromedp.Evaluate(
			`document.querySelector('input[id="option3"][name="personal_capacity"]')?.click()`, nil,
		))
	}
	sleep(500)

	commitExists := elemExists(ctx, `input[name="commit"]`)
	fmt.Printf("%s [DBG] commit button exists (immediate): %v\n", ts(), commitExists)
	waitTimeout(ctx, 15*time.Second, chromedp.WaitVisible(`input[name="commit"]`, chromedp.ByQuery))
	commitExists = elemExists(ctx, `input[name="commit"]`)
	fmt.Printf("%s [DBG] commit button exists (after wait): %v\n", ts(), commitExists)

	if commitExists {
		// Log button value/label
		var commitVal string
		chromedp.Run(ctx, chromedp.Evaluate(`document.querySelector('input[name="commit"]')?.value`, &commitVal))
		fmt.Printf("%s [DBG] commit button value: %q\n", ts(), commitVal)
		chromedp.Run(ctx, chromedp.Click(`input[name="commit"]`, chromedp.ByQuery))
		sleep(1000)
		chromedp.Run(ctx, chromedp.Location(&currentURL))
		fmt.Printf("%s [DBG] post-commit URL: %s\n", ts(), currentURL)
	} else {
		// Screenshot to show what's on screen
		var buf []byte
		chromedp.Run(ctx, chromedp.FullScreenshot(&buf, 90))
		os.WriteFile(filepath.Join(outputDir, "no-commit.png"), buf, 0644)
		fmt.Printf("%s [WARN] commit button not found; screenshot → no-commit.png\n", ts())
	}

	// Screenshot right before polling (shows state after commit click)
	{
		var buf []byte
		chromedp.Run(ctx, chromedp.FullScreenshot(&buf, 90))
		os.WriteFile(filepath.Join(outputDir, "pre-poll.png"), buf, 0644)
	}

	var ulfPath string
	ulfDest := filepath.Join(outputDir, ulfName)
	// Also check default Chrome download dirs in case SetDownloadBehavior had no effect
	homeDir, _ := os.UserHomeDir()
	pollDirs := []string{cwd, outputDir, filepath.Join(homeDir, "Downloads"), "/tmp"}
	for i := range 60 {
		sleep(500)
		if i%6 == 0 { // every 3s log dir contents
			chromedp.Run(ctx, chromedp.Location(&currentURL))
			fmt.Printf("%s [DBG] poll %d/60 URL: %s\n", ts(), i, currentURL)
			for _, d := range pollDirs {
				entries, err := os.ReadDir(d)
				if err != nil {
					continue
				}
				var names []string
				for _, e := range entries {
					names = append(names, e.Name())
				}
				if len(names) > 0 {
					fmt.Printf("%s [DBG] dir %s: %v\n", ts(), d, names)
				}
			}
		}
		for _, d := range pollDirs {
			entries, _ := os.ReadDir(d)
			for _, e := range entries {
				if !strings.HasSuffix(e.Name(), ".ulf") {
					continue
				}
				src := filepath.Join(d, e.Name())
				if src != ulfDest {
					os.Rename(src, ulfDest)
				}
				ulfPath = ulfDest
				break
			}
			if ulfPath != "" {
				break
			}
		}
		if ulfPath != "" {
			break
		}
	}
	if ulfPath != "" {
		fmt.Printf("%s [SUCCESS] ULF → %s\n", ts(), ulfPath)
	} else {
		var buf []byte
		chromedp.Run(ctx, chromedp.FullScreenshot(&buf, 90))
		os.WriteFile(filepath.Join(outputDir, "state.png"), buf, 0644)
		fmt.Printf("%s [WARN] no ULF after poll; state.png saved\n", ts())
	}
	return nil
}

// ── log filter ────────────────────────────────────────────────────────────────
// suppress noisy CDP unmarshal errors from chromedp
type logFilter struct{ w io.Writer }

func (f *logFilter) Write(p []byte) (int, error) {
	if strings.Contains(string(p), "could not unmarshal event") {
		return len(p), nil
	}
	return f.w.Write(p)
}

func main() {
	start := time.Now()
	log.SetOutput(&logFilter{w: os.Stderr})
	cwd, err := os.Getwd()
	if err != nil {
		log.Fatal(err)
	}
	flag.StringVar(&email, "email", "", "Unity ID email")
	flag.StringVar(&password, "password", "", "Unity ID password")
	flag.StringVar(&alfFile, "alf", "", "path to .alf license request file")
	flag.StringVar(&totpKey, "totp", "", "base32 TOTP secret (required if 2FA is enabled)")
	flag.StringVar(&ulfName, "ulf-name", "unity.ulf", "ULF output filename")
	flag.StringVar(&outputDir, "output", ".output", "directory for all output files (ULF, screenshots, HTML)")
	flag.StringVar(&dataDir, "data-dir", "", "persistent Chrome profile dir (reuses session, skips login)")
	flag.Parse()

	if email == "" || password == "" || alfFile == "" {
		flag.Usage()
		os.Exit(1)
	}

	if !filepath.IsAbs(alfFile) {
		alfFile = filepath.Join(cwd, alfFile)
	}
	if !filepath.IsAbs(outputDir) {
		outputDir = filepath.Join(cwd, outputDir)
	}
	os.MkdirAll(outputDir, 0755)

	// Auto-download Chrome if not in PATH (cache at ~/.cache/rod/browser)
	browserPath, err := launcher.NewBrowser().Get()
	if err != nil {
		log.Fatalf("browser download: %v", err)
	}
	fmt.Printf("%s [INFO] Chrome: %s\n", ts(), browserPath)

	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(browserPath),
		chromedp.Flag("headless", true),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-setuid-sandbox", true),
	)
	if dataDir != "" {
		if !filepath.IsAbs(dataDir) {
			dataDir = filepath.Join(cwd, dataDir)
		}
		opts = append(opts, chromedp.Flag("user-data-dir", dataDir))
		fmt.Printf("%s [INFO] Using persistent profile: %s\n", ts(), dataDir)
	}

	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), opts...)
	defer cancelAlloc()

	ctx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()
	// CDP download behavior — save downloaded files to outputDir
	if err := chromedp.Run(ctx,
		browser.SetDownloadBehavior(browser.SetDownloadBehaviorBehaviorAllow).
			WithDownloadPath(outputDir).
			WithEventsEnabled(true),
	); err != nil {
		fmt.Printf("%s [WARN] setDownloadBehavior: %v\n", ts(), err)
	}

	if err := run(ctx, cwd); err != nil {
		fmt.Printf("%s [ERROR] %v\n", ts(), err)
		var buf []byte
		chromedp.Run(ctx, chromedp.FullScreenshot(&buf, 90))
		os.WriteFile(filepath.Join(outputDir, "error.png"), buf, 0644)
		var html string
		chromedp.Run(ctx, chromedp.Evaluate(`document.documentElement.outerHTML`, &html))
		os.WriteFile(filepath.Join(outputDir, "error.html"), []byte(html), 0644)
	}
	fmt.Printf("%s [DONE] %.1fs\n", ts(), time.Since(start).Seconds())
}
