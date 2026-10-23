package xpay

import (
	"crypto/sha1"
	"sort"
	"strings"
)

func concatSorted(parts []string) string {
	cp := append([]string{}, parts...)
	sort.Strings(cp)
	return strings.Join(cp, "")
}

func sha1Sum(b []byte) []byte {
	s := sha1.Sum(b)
	return s[:]
}
