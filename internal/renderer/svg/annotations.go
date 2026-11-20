package svg

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mrmarble/termsvg/internal/annotate"
	"github.com/mrmarble/termsvg/internal/color"
)

// Annotations are an overlay drawn on top of the terminal content, inside the
// clipped content group, so they share the content's cell geometry no matter
// which layout, animation backend or frame switch the export uses. They never
// touch the recorded frames: with an empty track no annotation markup, style
// or keyframes are emitted and the output is byte-identical to before.

// annotationLabelPadX is the horizontal padding inside the label, in pixels.
const annotationLabelPadX = 4

func annotationAnimationName(index int) string { return "ann" + strconv.Itoa(index) }

// annotationVisibility returns the show/hide keyframes of one annotation over
// [From, To). An annotation covering the whole recording has no keyframes and
// is always visible.
func annotationVisibility(duration time.Duration, ann annotate.Annotation) []keyframePoint[bool] {
	points := []timelinePoint[bool]{{time: 0, state: ann.From <= 0}}
	if ann.From > 0 {
		points = append(points, timelinePoint[bool]{time: ann.From, state: true})
	}
	if ann.To < duration {
		points = append(points, timelinePoint[bool]{time: ann.To, state: false})
	}
	return normalizeTimeline(duration, points, func(a, b bool) bool { return a == b }).keyframes()
}

// annotationStyleRules paints the track from the theme: the highlight and the
// label plate use the foreground color, the label text the background color,
// so annotations stay readable on dark and light themes alike.
func (c *canvas) annotationStyleRules() string {
	fg := color.RGBAtoHex(c.config.Theme.Foreground)
	bg := color.RGBAtoHex(c.config.Theme.Background)
	return ".annh{fill:" + fg + ";fill-opacity:.18;stroke:" + fg + ";stroke-opacity:.7;stroke-width:2}" +
		".annb{fill:" + fg + "}" +
		".annt{fill:" + bg + "}"
}

func (c *canvas) writeAnnotation(w io.Writer, index int) {
	ann := c.rec.Annotations[index]
	frames := annotationVisibility(c.plan.duration, ann)
	initial := ""
	if len(frames) > 0 && !frames[0].state {
		initial = ` visibility="hidden"`
	}
	if len(frames) > 1 && c.options.Animation == AnimationCSS {
		fmt.Fprintf(w, `<g style="animation:%s %s %s step-end"%s>`, annotationAnimationName(index),
			animationDuration(c.plan.duration), c.loopCount().css(), initial)
	} else {
		fmt.Fprintf(w, `<g%s>`, initial)
	}
	if len(frames) > 1 && c.options.Animation == AnimationSMIL {
		values := make([]string, len(frames))
		for i, frame := range frames {
			values[i] = layerVisibilityValue(frame.state)
		}
		c.smil().writeSMILAnimation(w, "animate", []smilAttribute{
			{name: "attributeName", value: "visibility"},
			{name: "values", value: strings.Join(values, ";")},
			{name: "keyTimes", value: smilKeyTimes(frames)},
		})
	}
	fmt.Fprintf(w, `<rect class="annh" x="%s" y="%s" width="%s" height="%s"/>`,
		c.xmlInt(ann.Col*ColWidth), c.xmlInt(ann.Row*RowHeight),
		c.xmlInt(ann.Cols*ColWidth), c.xmlInt(ann.Rows*RowHeight))
	if ann.Text != "" {
		c.writeAnnotationLabel(w, &ann)
	}
	fmt.Fprint(w, `</g>`)
}

// writeAnnotationLabel draws the label plate below the highlight, or above it
// when there is no room below. The plate is shifted left to stay on the
// canvas; on a canvas too small either way it overlaps the highlight.
func (c *canvas) writeAnnotationLabel(w io.Writer, ann *annotate.Annotation) {
	text := ann.Text
	if c.config.Minify {
		// Replace spaces with non-breaking spaces to survive minification, as
		// with terminal text; svgoutput restores them after minification.
		text = strings.ReplaceAll(text, " ", " ")
	}
	width := utf8.RuneCountInString(ann.Text)*ColWidth + 2*annotationLabelPadX
	x := ann.Col * ColWidth
	if x+width > c.contentWidth() {
		x = max(0, c.contentWidth()-width)
	}
	row := ann.Row + ann.Rows
	if (row+1)*RowHeight > c.contentHeight() {
		row = ann.Row - 1
	}
	if row < 0 {
		row = ann.Row
	}
	y := row * RowHeight
	fmt.Fprintf(w, `<rect class="annb" x="%s" y="%s" width="%s" height="%s" rx="3"/>`,
		c.xmlInt(x), c.xmlInt(y), c.xmlInt(width), c.xmlInt(RowHeight))
	fmt.Fprintf(w, `<text class="annt" x="%s" y="%s">%s</text>`,
		c.xmlInt(x+annotationLabelPadX), c.xmlInt(y+RowHeight-5), svgTextEscaper.Replace(text))
}

func (c *canvas) generateAnnotationKeyframes(index int) string {
	frames := annotationVisibility(c.plan.duration, c.rec.Annotations[index])
	if len(frames) <= 1 {
		return ""
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "@keyframes %s{", annotationAnimationName(index))
	for _, frame := range frames {
		fmt.Fprintf(&sb, "%s{visibility:%s}", frame.selector, layerVisibilityValue(frame.state))
	}
	sb.WriteString("}")
	return sb.String()
}
