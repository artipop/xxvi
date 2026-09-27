package launch

import (
	"regexp"
	"strings"
)

var (
	ansi     = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)`)
	localURL = regexp.MustCompile(`https?://(localhost|127\.0\.0\.1|0\.0\.0\.0|\[::1?\]|[\w.-]+\.localhost)(:\d+)?(/[^\s'"<>)\]]*)?`)
)

// Address is where a started project says it can be reached: the first local
// address in what it printed. Read from the output rather than trusted from
// the profile, because a busy port moves a dev server to the next one and says
// so only there.
//
// 0.0.0.0 is where a server listens, not somewhere to go, and a browser that
// is sent there is refused on some systems — it is rewritten to localhost.
func Address(output []byte) string {
	text := ansi.ReplaceAllString(string(output), "")
	m := localURL.FindString(text)
	if m == "" {
		return ""
	}
	m = strings.TrimRight(m, ".,;:")
	return strings.Replace(m, "://0.0.0.0", "://localhost", 1)
}
