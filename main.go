// Unity License Activation — automated via playwright-go.
//
// Usage:
//
//	go run . --email <email> --password <pass> --alf <file> [flags]
//
// Flags:
//
//	--email         Unity ID email (required)
//	--password      Unity ID password (required)
//	--alf           Path to .alf license request file (required)
//	--totp          Base32 TOTP secret (required if 2FA is enabled)
//	--ulf-name      ULF output filename (default: unity.ulf)
//	--output        Output directory for ULF, error dumps (default: .output)
//	--data-dir      Persistent Chrome profile dir — reuses session on subsequent
//	                runs, skipping login (~18s vs ~34s). Created automatically.
//	--skip-install  Skip playwright browser installation check (use when browser
//	                is pre-installed in Docker image to save ~10–15s per run).
//
// Output directory (--output) contains:
//
//	{ulf-name}      Downloaded license file
//	error.png       Screenshot on crash
//	error.html      Page HTML on crash
//
// Parameters are validated early (before starting the browser) to avoid
// wasting time/resources on invalid input.
//
// Flow: navigate → [login + TFA (if needed) →] ToS accept → upload ALF
//       → [re-login + retry on session expiry] → select license → download ULF.
// If the server redirects to login after ALF submit (cached-page session expiry),
// the tool re-logs in and retries the upload automatically.
// With --data-dir: subsequent runs skip login (session reuse ~18s total).
// Exits non-zero on error.
package main

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/binary"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	playwright "github.com/mxschmitt/playwright-go"
)

// ── globals ───────────────────────────────────────────────────────────────────

var (
	email       string
	password    string
	alfFile     string
	totpKey     string
	ulfName     string
	outputDir   string
	dataDir     string
	skipInstall bool
)

// ── helpers ───────────────────────────────────────────────────────────────────

func ts() string {
	d := time.Now()
	return fmt.Sprintf("%04d/%02d/%02d %02d:%02d:%02d",
		d.Year(), int(d.Month()), d.Day(), d.Hour(), d.Minute(), d.Second())
}

func sleep(ms int) { time.Sleep(time.Duration(ms) * time.Millisecond) }

// ── TOTP (RFC 6238) ───────────────────────────────────────────────────────────

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

func totpCode(key string) (string, error) {
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

// ── browser helpers ───────────────────────────────────────────────────────────

func fill(page playwright.Page, sel, val string) error {
	return page.Locator(sel).Fill(val, playwright.LocatorFillOptions{
		Timeout: playwright.Float(15000),
	})
}

func elemExists(page playwright.Page, sel string) bool {
	n, err := page.Locator(sel).Count()
	return err == nil && n > 0
}

func waitVisible(page playwright.Page, sel string, timeoutMs float64) error {
	return page.Locator(sel).First().WaitFor(playwright.LocatorWaitForOptions{
		State:   playwright.WaitForSelectorStateVisible,
		Timeout: playwright.Float(timeoutMs),
	})
}

func snap(page playwright.Page, name string) {
	page.Screenshot(playwright.PageScreenshotOptions{
		Path:     playwright.String(filepath.Join(outputDir, name)),
		FullPage: playwright.Bool(true),
	})
}

// pollFor polls every 200 ms for up to timeoutMs.
// Returns ("url", matchedURL) if the page URL contains any abortOnURL entry.
// Returns ("sel", matchedSel) if any selector in selectors appears in the DOM.
// Returns ("timeout", "") if the deadline passes with neither condition met.
func pollFor(page playwright.Page, timeoutMs int, abortOnURL []string, selectors ...string) (reason, val string) {
	deadline := time.Now().Add(time.Duration(timeoutMs) * time.Millisecond)
	for time.Now().Before(deadline) {
		sleep(200)
		url := page.URL()
		for _, u := range abortOnURL {
			if strings.Contains(url, u) {
				return "url", url
			}
		}
		for _, sel := range selectors {
			if elemExists(page, sel) {
				return "sel", sel
			}
		}
	}
	return "timeout", ""
}

// ── login ─────────────────────────────────────────────────────────────────────

// doLogin performs the full Unity email+password+2FA+ToS login flow.
// The page must be on login.unity.com on entry.
func doLogin(page playwright.Page) error {
	if err := waitVisible(page, `input[name="email"]`, 20000); err != nil {
		return fmt.Errorf("email input not found: %w", err)
	}

	// Accept cookies banner if present
	res, _ := page.Evaluate(`
		(function() {
			const a = Array.from(document.querySelectorAll("button"))
			             .find(b => b.textContent.trim() === "Accept All Cookies");
			if (a) { a.click(); return true; }
			return false;
		})()
	`)
	if accepted, _ := res.(bool); accepted {
		fmt.Printf("%s [INFO] Cookies accepted\n", ts())
		sleep(500)
	}

	// Email
	fmt.Printf("%s [INFO] Filling email...\n", ts())
	if err := fill(page, `input[name="email"]`, email); err != nil {
		return fmt.Errorf("fill email: %w", err)
	}
	page.Evaluate(`document.querySelector('button[type="submit"]').click()`)

	// Password
	if err := waitVisible(page, `input[type="password"]`, 15000); err != nil {
		return fmt.Errorf("password field not found: %w", err)
	}
	fmt.Printf("%s [INFO] Filling password...\n", ts())
	if err := fill(page, `input[type="password"]`, password); err != nil {
		return fmt.Errorf("fill password: %w", err)
	}
	page.Evaluate(`document.querySelector('button[type="submit"]').click()`)

	// Wait for TFA page or landing after login
	pollFor(page, 15000, nil,
		`input[autocomplete="one-time-code"]`,
		`input[name="licenseFile"]`,
	)
	currentURL := page.URL()
	fmt.Printf("%s [INFO] Post-login URL: %s\n", ts(), currentURL)

	// 2FA
	if strings.Contains(currentURL, "sign-in/tfa") || elemExists(page, `input[autocomplete="one-time-code"]`) {
		if totpKey == "" {
			return fmt.Errorf("2FA required but no --totp key provided")
		}
		code, err := totpCode(totpKey)
		if err != nil {
			return fmt.Errorf("totp: %w", err)
		}
		fmt.Printf("%s [INFO] 2FA TOTP code: %s***\n", ts(), code[:3])

		tfaSel := `input[autocomplete="one-time-code"]`
		if !elemExists(page, tfaSel) {
			tfaSel = `input[name="conversations_tfa_required_form\[verify_code\]"]`
		}
		if !elemExists(page, tfaSel) {
			tfaSel = `input:not([type="hidden"]):not([type="checkbox"]):not([name="email"])`
		}
		if elemExists(page, tfaSel) {
			page.Locator(tfaSel).First().Fill(code)
			if elemExists(page, `button[type="submit"]`) {
				page.Evaluate(`document.querySelector('button[type="submit"]').click()`)
			}
		}
		pollFor(page, 12000, nil, `input[name="licenseFile"]`)
		currentURL = page.URL()
		if !strings.Contains(currentURL, "license.unity3d.com") {
			return fmt.Errorf("TFA redirect timeout — stuck at %s", currentURL)
		}
	}

	// ToS
	tosSel := `button[name="conversations_accept_updated_tos_form\[accept\]"]`
	if elemExists(page, tosSel) {
		fmt.Printf("%s [INFO] ToS accepted\n", ts())
		page.Locator(tosSel).Click()
		page.WaitForLoadState(playwright.PageWaitForLoadStateOptions{State: playwright.LoadStateDomcontentloaded})
		sleep(500)
	}

	return nil
}

// ── license page ──────────────────────────────────────────────────────────────

// navigateToLicense navigates to the manual license page and waits for the upload field.
func navigateToLicense(page playwright.Page) error {
	page.Goto("https://license.unity3d.com/manual")
	page.WaitForLoadState(playwright.PageWaitForLoadStateOptions{State: playwright.LoadStateDomcontentloaded})
	if err := waitVisible(page, `input[name="licenseFile"]`, 15000); err != nil {
		return fmt.Errorf("license upload field not found after navigation: %w", err)
	}
	return nil
}

// ── ALF upload ────────────────────────────────────────────────────────────────

// errSessionExpired is a sentinel string embedded in errors returned by uploadALF
// when the server redirects to login.unity.com after the form submit.
const errSessionExpired = "session-expired"

// uploadALF uploads the ALF file and clicks the commit button.
// Returns an error wrapping errSessionExpired if the server redirects to login (cached-page
// auth failure). The caller should re-login and call uploadALF again.
func uploadALF(page playwright.Page, alfPath string) error {
	if !elemExists(page, `input[name="licenseFile"]`) {
		return fmt.Errorf("no licenseFile input on %s", page.URL())
	}

	fmt.Printf("%s [INFO] Uploading ALF...\n", ts())
	if err := page.Locator(`input[name="licenseFile"]`).SetInputFiles(alfPath); err != nil {
		return fmt.Errorf("upload ALF: %w", err)
	}

	if !elemExists(page, `input[name="commit"]`) {
		return nil // some Unity versions auto-advance without a commit button
	}
	page.Locator(`input[name="commit"]`).Click()

	// Wait for license type selection page, but abort immediately if the server
	// redirects back to login (= session was invalid for POST even though GET worked).
	reason, val := pollFor(page, 10000,
		[]string{"login.unity.com"},
		`input[id="type_personal"]`,
	)
	switch reason {
	case "url":
		return fmt.Errorf("[%s] redirected to %s after ALF commit", errSessionExpired, val)
	case "timeout":
		// Final URL check
		if strings.Contains(page.URL(), "login.unity.com") {
			return fmt.Errorf("[%s] redirected to %s after ALF commit (post-timeout)", errSessionExpired, page.URL())
		}
	}
	return nil
}

// ── main flow ─────────────────────────────────────────────────────────────────

func run(page playwright.Page) error {
	var currentURL string

	// ── Phase 1: Navigate to license page ────────────────────────────────────
	fmt.Printf("%s [INFO] Loading license page...\n", ts())
	page.Goto("https://license.unity3d.com/manual")

	// Poll: redirect to login OR upload field appears.
	reason, _ := pollFor(page, 12000,
		[]string{"login.unity.com"},
		`input[name="licenseFile"]`,
	)
	needsLogin := reason == "url" || strings.Contains(page.URL(), "login.unity.com")

	if needsLogin {
		fmt.Printf("%s [INFO] Session expired, logging in...\n", ts())
		if err := doLogin(page); err != nil {
			return fmt.Errorf("login: %w", err)
		}
		if !strings.Contains(page.URL(), "license.unity3d.com") {
			if err := navigateToLicense(page); err != nil {
				return err
			}
		}
	}

	currentURL = page.URL()
	fmt.Printf("%s [2 on license] %s\n", ts(), currentURL)

	// ToS can also appear directly on the license page (first domain visit)
	tosSel := `button[name="conversations_accept_updated_tos_form\[accept\]"]`
	if elemExists(page, tosSel) {
		fmt.Printf("%s [INFO] ToS accepted\n", ts())
		page.Locator(tosSel).Click()
		page.WaitForLoadState(playwright.PageWaitForLoadStateOptions{State: playwright.LoadStateDomcontentloaded})
	}

	// Verify upload field present before proceeding
	if !elemExists(page, `input[name="licenseFile"]`) {
		res, _ := page.Evaluate(`document.body.textContent`)
		body, _ := res.(string)
		if len(body) > 500 {
			body = body[:500]
		}
		fmt.Printf("%s Page (%s): %s\n", ts(), page.URL(), body)
		return fmt.Errorf("upload field not found; page: %s", strings.TrimSpace(body))
	}

	// ── Phase 2: Upload ALF ───────────────────────────────────────────────────
	// FIX: detect POST-level session expiry (Unity allows GET /manual without auth,
	// but the form submit requires a valid session — persistent profiles on Docker
	// can have a cached page with a stale/missing session cookie).
	alfPath := alfFile
	if err := uploadALF(page, alfPath); err != nil {
		if !strings.Contains(err.Error(), errSessionExpired) {
			return err
		}
		// Cached-page auth failure → re-login then retry upload once.
		fmt.Printf("%s [INFO] %v — re-logging in and retrying...\n", ts(), err)
		if err2 := doLogin(page); err2 != nil {
			return fmt.Errorf("re-login after session expiry: %w", err2)
		}
		if !strings.Contains(page.URL(), "license.unity3d.com") {
			if err2 := navigateToLicense(page); err2 != nil {
				return err2
			}
		}
		if err2 := uploadALF(page, alfPath); err2 != nil {
			return fmt.Errorf("re-upload ALF: %w", err2)
		}
	}

	// ── Phase 3: Select personal license ─────────────────────────────────────
	fmt.Printf("%s [INFO] Selecting license type...\n", ts())
	currentURL = page.URL()
	fmt.Printf("%s [DBG] pre-select URL: %s\n", ts(), currentURL)

	personalExists := elemExists(page, `input[id="type_personal"][value="personal"]`)
	fmt.Printf("%s [DBG] type_personal exists: %v\n", ts(), personalExists)
	if personalExists {
		page.Evaluate(`document.querySelector('input[id="type_personal"][value="personal"]')?.click()`)
	} else {
		res, _ := page.Evaluate(`Array.from(document.querySelectorAll('input[type="radio"]')).map(e=>e.id+'/'+e.name+'/'+e.value).join(', ')`)
		radios, _ := res.(string)
		fmt.Printf("%s [DBG] radios on page: %s\n", ts(), radios)
	}
	sleep(300)

	option3Exists := elemExists(page, `input[id="option3"][name="personal_capacity"]`)
	fmt.Printf("%s [DBG] option3 exists: %v\n", ts(), option3Exists)
	if option3Exists {
		page.Evaluate(`document.querySelector('input[id="option3"][name="personal_capacity"]')?.click()`)
	}
	sleep(300)

	// ── Phase 4: Submit and download ULF ─────────────────────────────────────
	commitSel := `input.btn.mb10[name="commit"]`
	commitExists := elemExists(page, commitSel)
	fmt.Printf("%s [DBG] commit button exists (immediate): %v\n", ts(), commitExists)
	if !commitExists {
		// FIX: reduced from 15 s to 8 s — radio click rarely needs more than ~2 s to reveal button
		if err := waitVisible(page, commitSel, 8000); err == nil {
			commitExists = true
		}
	}
	fmt.Printf("%s [DBG] commit button exists (after wait): %v\n", ts(), commitExists)

	if !commitExists {
		return fmt.Errorf("commit button not found after license selection")
	}

	res, _ := page.Evaluate(`document.querySelector('input.btn.mb10')?.value`)
	commitVal, _ := res.(string)
	fmt.Printf("%s [DBG] commit button value: %q\n", ts(), commitVal)

	page.Evaluate(`document.querySelector('input.btn.mb10')?.click()`)
	sleep(1000)

	currentURL = page.URL()
	fmt.Printf("%s [DBG] post-commit URL: %s\n", ts(), currentURL)

	ulfDest := filepath.Join(outputDir, ulfName)
	download, err := page.ExpectDownload(func() error {
		page.Evaluate(`document.querySelector('input[value="Download license file"]')?.click()`)
		return nil
	}, playwright.PageExpectDownloadOptions{Timeout: playwright.Float(30000)})
	if err != nil {
		return fmt.Errorf("ULF download timed out")
	}
	if err := download.SaveAs(ulfDest); err != nil {
		return fmt.Errorf("save ulf: %w", err)
	}
	fmt.Printf("%s [SUCCESS] ULF → %s\n", ts(), ulfDest)
	return nil
}

// ── main ──────────────────────────────────────────────────────────────────────

func main() {
	start := time.Now()
	cwd, err := os.Getwd()
	if err != nil {
		log.Fatal(err)
	}

	flag.StringVar(&email, "email", "", "Unity ID email")
	flag.StringVar(&password, "password", "", "Unity ID password")
	flag.StringVar(&alfFile, "alf", "", "path to .alf license request file")
	flag.StringVar(&totpKey, "totp", "", "base32 TOTP secret (required if 2FA is enabled)")
	flag.StringVar(&ulfName, "ulf-name", "unity.ulf", "ULF output filename")
	flag.StringVar(&outputDir, "output", ".output", "directory for output files (ULF, error dumps)")
	flag.StringVar(&dataDir, "data-dir", "", "persistent Chrome profile dir (reuses session, skips login)")
	flag.BoolVar(&skipInstall, "skip-install", false, "skip playwright browser installation check (saves ~10–15s when browser is pre-installed)")
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

	// Validate inputs before starting browser (fail fast)
	if _, err := os.Stat(alfFile); err != nil {
		log.Fatalf("alf file not found: %v", err)
	}
	if totpKey != "" {
		for _, c := range strings.ToUpper(totpKey) {
			if !strings.ContainsRune(b32Alphabet, c) {
				log.Fatalf("totp: invalid base32 character %q", c)
			}
		}
		if len(totpKey) < 16 {
			log.Fatalf("totp: key too short (%d chars, need ≥16)", len(totpKey))
		}
	}
	if dataDir != "" && !filepath.IsAbs(dataDir) {
		dataDir = filepath.Join(cwd, dataDir)
	}

	// --skip-install: skip browser download (pre-installed browsers, e.g. Docker).
	if !skipInstall {
		if err := playwright.Install(&playwright.RunOptions{Browsers: []string{"chromium"}}); err != nil {
			log.Fatalf("playwright install: %v", err)
		}
	}

	pw, err := playwright.Run()
	if err != nil {
		log.Fatalf("playwright run: %v", err)
	}
	defer pw.Stop()

	launchOpts := playwright.BrowserTypeLaunchPersistentContextOptions{
		Headless:        playwright.Bool(true),
		AcceptDownloads: playwright.Bool(true),
		Args:            []string{"--no-sandbox", "--disable-setuid-sandbox"},
	}

	var context playwright.BrowserContext
	if dataDir != "" {
		fmt.Printf("%s [INFO] Using persistent profile: %s\n", ts(), dataDir)
		context, err = pw.Chromium.LaunchPersistentContext(dataDir, launchOpts)
	} else {
		context, err = pw.Chromium.LaunchPersistentContext("", launchOpts)
	}
	if err != nil {
		log.Fatalf("launch: %v", err)
	}
	defer context.Close()

	page, err := context.NewPage()
	if err != nil {
		log.Fatalf("new page: %v", err)
	}

	if err := run(page); err != nil {
		fmt.Printf("%s [ERROR] %v\n", ts(), err)
		snap(page, "error.png")
		if html, e := page.Content(); e == nil {
			os.WriteFile(filepath.Join(outputDir, "error.html"), []byte(html), 0644)
		}
		os.Exit(1)
	}
	fmt.Printf("%s [DONE] %.1fs\n", ts(), time.Since(start).Seconds())
}
