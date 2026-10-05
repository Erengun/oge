package oracle

import "path"

// Match reports whether the slash-separated path matches pattern, where
// "**" as a whole segment matches zero or more segments and other segments
// follow path.Match. A malformed pattern matches nothing.
func Match(pattern, name string) bool {
	return matchSegs(split(pattern), split(name))
}

func matchSegs(pat, name []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			for i := 0; i <= len(name); i++ {
				if matchSegs(pat[1:], name[i:]) {
					return true
				}
			}
			return false
		}
		if len(name) == 0 {
			return false
		}
		if ok, err := path.Match(pat[0], name[0]); err != nil || !ok {
			return false
		}
		pat, name = pat[1:], name[1:]
	}
	return len(name) == 0
}

func split(p string) []string {
	var segs []string
	start := 0
	for i := 0; i <= len(p); i++ {
		if i == len(p) || p[i] == '/' {
			if i > start {
				segs = append(segs, p[start:i])
			}
			start = i + 1
		}
	}
	return segs
}

// MatchAny reports whether name matches any of patterns.
func MatchAny(patterns []string, name string) bool {
	for _, p := range patterns {
		if Match(p, name) {
			return true
		}
	}
	return false
}
