package service

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

// Animation output is a separate, script-free document. Never relax the
// existing static thumbnail sanitizer or publish raw model HTML.
type DegradationAnimation struct {
	Document string `json:"document"`
	Animated bool   `json:"animated"`
}

var animationLocalReference = regexp.MustCompile(`^#[A-Za-z_][A-Za-z0-9_.:-]*$`)
var animationCSSURL = regexp.MustCompile(`(?i)url\s*\(\s*['"]?#[A-Za-z_][A-Za-z0-9_.:-]*['"]?\s*\)`)

func animationCSS(raw string) string {
	if len(raw) > 64<<10 || strings.ContainsAny(raw, "<>\\\x00") {
		return ""
	}
	// Fragment paint servers are fine; external URLs/imports are not.
	lower := strings.ToLower(animationCSSURL.ReplaceAllString(raw, ""))
	for _, denied := range []string{"url", "@import", "expression(", "javascript:", "http:", "https:", "data:", "image-set("} {
		if strings.Contains(lower, denied) {
			return ""
		}
	}
	return raw
}

func PrepareDegradationAnimation(output string) (*DegradationAnimation, error) {
	if len(output) > 512<<10 {
		return nil, errors.New("animation source too large")
	}
	start, end := strings.Index(output, "<svg"), strings.LastIndex(output, "</svg>")
	if start < 0 || end < start {
		return nil, errors.New("missing SVG")
	}
	raw := output[start : end+6]
	if len(raw) > 128<<10 {
		return nil, errors.New("animation SVG too large")
	}
	// Style blocks may live in the HTML head rather than inside the SVG.
	doc, err := html.Parse(strings.NewReader(output))
	if err != nil {
		return nil, err
	}
	var css strings.Builder
	var collect func(*html.Node)
	collect = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "style" {
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				if c.Type == html.TextNode && css.Len()+len(c.Data) < 64<<10 {
					css.WriteString(animationCSS(c.Data))
					css.WriteByte('\n')
				}
			}
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			collect(c)
		}
	}
	collect(doc)
	allowed, attrs := map[string]bool{}, map[string]bool{}
	for _, key := range intelligentSVGElements {
		allowed[key] = true
	}
	for _, key := range strings.Fields("use animate animateTransform animateMotion mpath filter feGaussianBlur feDropShadow feOffset feMerge feMergeNode feColorMatrix feBlend") {
		allowed[key] = true
	}
	for _, key := range intelligentSVGAttributes {
		attrs[key] = true
	}
	for _, key := range strings.Fields("class style href attributeName attributeType from to by values dur begin end repeatCount repeatDur fill calcMode keyTimes keySplines keyPoints additive accumulate type path rotate filter stdDeviation in in2 result mode flood-color flood-opacity") {
		attrs[key] = true
	}
	targets := map[string]bool{}
	for _, key := range strings.Fields("transform opacity fill fill-opacity stroke stroke-width stroke-opacity stroke-dashoffset x y x1 x2 y1 y2 cx cy r rx ry width height d points offset stop-color stop-opacity") {
		targets[key] = true
	}
	var svg bytes.Buffer
	encoder := xml.NewEncoder(&svg)
	decoder := xml.NewDecoder(strings.NewReader(raw))
	depth, skip, nodes, drawable := 0, 0, 0, 0
	animated := false
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := token.(type) {
		case xml.StartElement:
			depth++
			nodes++
			if depth > 40 || nodes > 6000 {
				return nil, errors.New("animation SVG too complex")
			}
			if skip > 0 {
				skip++
				continue
			}
			name := t.Name.Local
			ok := allowed[name] && (t.Name.Space == "" || t.Name.Space == "http://www.w3.org/2000/svg")
			isAnimation := name == "animate" || name == "animateTransform" || name == "animateMotion"
			if name == "animate" || name == "animateTransform" {
				target := ""
				for _, a := range t.Attr {
					if a.Name.Local == "attributeName" {
						target = a.Value
					}
				}
				ok = ok && targets[target]
			}
			if !ok {
				skip = 1
				continue
			}
			element := xml.StartElement{Name: xml.Name{Local: name}}
			for _, a := range t.Attr {
				if !attrs[a.Name.Local] || (a.Name.Space != "" && !(a.Name.Space == "http://www.w3.org/1999/xlink" && a.Name.Local == "href")) {
					continue
				}
				value := a.Value
				if a.Name.Local == "style" {
					value = animationCSS(value)
				} else if a.Name.Local == "href" {
					if !animationLocalReference.MatchString(value) {
						continue
					}
				} else if !svgStaticValue(value) {
					continue
				}
				if value != "" {
					element.Attr = append(element.Attr, xml.Attr{Name: xml.Name{Local: a.Name.Local}, Value: value})
					if a.Name.Local == "style" && strings.Contains(strings.ToLower(value), "animation") {
						animated = true
					}
				}
			}
			if depth == 1 {
				element.Attr = append(element.Attr, xml.Attr{Name: xml.Name{Local: "xmlns"}, Value: "http://www.w3.org/2000/svg"})
			}
			if strings.Contains(" path rect circle ellipse line polyline polygon ", " "+name+" ") {
				drawable++
			}
			animated = animated || isAnimation
			if err := encoder.EncodeToken(element); err != nil {
				return nil, err
			}
		case xml.EndElement:
			depth--
			if skip > 0 {
				skip--
				continue
			}
			if err := encoder.EncodeToken(xml.EndElement{Name: xml.Name{Local: t.Name.Local}}); err != nil {
				return nil, err
			}
		case xml.CharData:
			if skip == 0 {
				if err := encoder.EncodeToken(t); err != nil {
					return nil, err
				}
			}
		case xml.Comment:
		default:
			return nil, errors.New("animation SVG directives are not supported")
		}
	}
	if err := encoder.Flush(); err != nil {
		return nil, err
	}
	if drawable == 0 || depth != 0 || svg.Len() > 256<<10 {
		return nil, errors.New("invalid animation SVG")
	}
	animated = animated || strings.Contains(strings.ToLower(css.String()), "@keyframes")
	document := `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta http-equiv="Content-Security-Policy" content="default-src 'none'; script-src 'none'; style-src 'unsafe-inline'; img-src 'none'; connect-src 'none'; font-src 'none'; frame-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'"><style>` +
		css.String() + `</style><style>html,body{margin:0;padding:0;width:100%;height:100%;overflow:hidden}body{display:grid;place-items:center}svg{display:block;max-width:100%;max-height:100%;width:100%;height:100%}</style></head><body>` + svg.String() + `</body></html>`
	return &DegradationAnimation{Document: document, Animated: animated}, nil
}
