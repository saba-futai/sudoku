package logx

import (
	"log"
	"strings"
	"sync"
)

var stdInstallOnce sync.Once

// InstallStd routes the standard library's global logger through logx formatting.
// It is safe to call multiple times.
func InstallStd() {
	stdInstallOnce.Do(func() {
		log.SetFlags(0)
		log.SetPrefix("")
		log.SetOutput(stdWriter{})
	})
}

type stdWriter struct{}

func (stdWriter) Write(p []byte) (int, error) {
	msg := strings.TrimRight(string(p), "\r\n")
	component, rest := parseLeadingComponents(msg)
	if strings.TrimSpace(rest) == "" {
		rest = msg
	}
	logf(LevelInfo, component, "%s", rest)
	return len(p), nil
}

func parseLeadingComponents(s string) (string, string) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "[") {
		return "", s
	}

	components := make([]string, 0, 4)
	rest := s
	for strings.HasPrefix(rest, "[") {
		end := strings.IndexByte(rest, ']')
		if end <= 1 {
			break
		}
		seg := strings.TrimSpace(rest[1:end])
		if seg == "" {
			break
		}
		components = append(components, seg)
		rest = strings.TrimSpace(rest[end+1:])
	}

	if len(components) == 0 {
		return "", s
	}
	return strings.Join(components, "/"), rest
}
