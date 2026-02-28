package metrics

import (
	"fmt"
	"sort"
	"strings"
)

func FormatPrometheus(samples []Sample) string {
	var b strings.Builder
	seen := make(map[string]struct{}, 64)

	for _, s := range samples {
		if _, ok := seen[s.Name]; !ok {
			b.WriteString("# HELP ")
			b.WriteString(s.Name)
			b.WriteByte(' ')
			b.WriteString(escapeHelp(s.Help))
			b.WriteByte('\n')

			b.WriteString("# TYPE ")
			b.WriteString(s.Name)
			b.WriteByte(' ')
			b.WriteString(string(s.Type))
			b.WriteByte('\n')
			seen[s.Name] = struct{}{}
		}

		b.WriteString(s.Name)
		if len(s.Labels) > 0 {
			b.WriteByte('{')
			keys := make([]string, 0, len(s.Labels))
			for k := range s.Labels {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for i, k := range keys {
				if i > 0 {
					b.WriteByte(',')
				}
				b.WriteString(k)
				b.WriteString("=\"")
				b.WriteString(escapeLabel(s.Labels[k]))
				b.WriteByte('"')
			}
			b.WriteByte('}')
		}
		b.WriteByte(' ')
		b.WriteString(fmt.Sprintf("%.17g", s.Value))
		b.WriteByte('\n')
	}

	return b.String()
}

func escapeHelp(v string) string {
	v = strings.ReplaceAll(v, "\\", "\\\\")
	return strings.ReplaceAll(v, "\n", "\\n")
}

func escapeLabel(v string) string {
	v = strings.ReplaceAll(v, "\\", "\\\\")
	v = strings.ReplaceAll(v, "\"", "\\\"")
	return strings.ReplaceAll(v, "\n", "\\n")
}
