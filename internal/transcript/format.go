// Package transcript formats the merged, editable transcript view shared by
// the pipeline and server packages.
package transcript

import (
	"fmt"
	"strings"

	"localmeetassist/internal/model"
)

// Format renders segments as Markdown lines of the form
// "[mm:ss] Speaker: text". Callers that require a specific ordering sort the
// segments before calling Format.
func Format(segments []model.Segment, names map[string]string) string {
	var builder strings.Builder
	for _, segment := range segments {
		name := names[segment.SpeakerID]
		if name == "" {
			name = segment.SpeakerID
		}
		fmt.Fprintf(&builder, "[%02d:%02d] %s: %s\n", segment.StartMS/60000, (segment.StartMS/1000)%60, name, strings.TrimSpace(segment.Text))
	}
	return strings.TrimSpace(builder.String())
}
