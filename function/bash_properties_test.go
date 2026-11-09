package function

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestBashOutputRingPreservesOrder(t *testing.T) {
	// Distinct records expose misplaced/duplicated ring segments that repeated
	// padding cannot. Include several wraps and boundaries around the byte cap.
	const capacity = 1 << 15
	var pattern strings.Builder
	for i := 0; pattern.Len() < 4*capacity; i++ {
		fmt.Fprintf(&pattern, "%08x:%08x\n", i, uint32(i)*2654435761)
	}
	for _, size := range []int{0, 1, capacity - 1, capacity, capacity + 1, 2 * capacity, 3*capacity + 137} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			output := pattern.String()[:size]
			path := filepath.Join(t.TempDir(), "output")
			writeFixture(t, path, []byte(output))
			for _, mode := range []string{"bulk", "fragmented stdout and stderr"} {
				t.Run(mode, func(t *testing.T) {
					command := "cat -- " + shellQuote(path)
					if mode != "bulk" {
						// read -N preserves newlines in this ASCII fixture. Alternate
						// descriptors, retaining the final incomplete block as well.
						command = "i=0; while :; do chunk=; IFS= read -r -N 113 chunk; status=$?; " +
							"if ((i % 2)); then printf '%s' \"$chunk\" >&2; else printf '%s' \"$chunk\"; fi; " +
							"((i++)); ((status == 0)) || break; done < " + shellQuote(path)
					}
					ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
					defer cancel()
					result := invoke(t, &Bash{}, ctx, map[string]any{"command": command})
					requireSuccess(t, result)
					assertAttribute(t, result, "exit_code", "0")
					assertAttribute(t, result, "truncated", fmt.Sprint(size > capacity))
					want := output[max(0, size-capacity):]
					if len(want) > 0 && len(want) < capacity && !strings.HasSuffix(want, "\n") {
						want += "\n"
					}
					if result.body != "\n"+want {
						t.Fatalf("retained output differs from suffix model: got %d bytes, want %d", len(result.body), len(want)+1)
					}
				})
			}
		})
	}
}
