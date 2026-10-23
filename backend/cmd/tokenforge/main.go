// Command tokenforge mints a test user token for local smoke testing
// using the same HMAC formula as internal/api. Build tags keep it out
// of the server binary path; it is a developer tool only.
package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"
)

func main() {
	env := flag.String("env", "dianjing.env", "config file")
	user := flag.Int64("u", 1, "user id")
	days := flag.Int("d", 1, "valid days")
	flag.Parse()
	appKey, appSecret := "", ""
	data, _ := os.ReadFile(*env)
	for _, line := range strings.Split(string(data), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch k {
		case "XPAY_APP_KEY":
			appKey = v
		case "WX_APP_SECRET":
			appSecret = v
		}
	}
	secret := appKey + "|" + appSecret + "|dianjing-token"
	expires := fmt.Sprintf("%d", time.Now().Add(time.Duration(*days)*24*time.Hour).Unix())
	payload := fmt.Sprintf("%d.%s", *user, expires)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	fmt.Println(payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)))
}
