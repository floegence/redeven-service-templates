package template

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
)

func ValidateSVG(reference IconReference, data []byte) error {
	if len(data) == 0 || len(data) > 64*1024 || reference.MediaType != "image/svg+xml" {
		return errors.New("SVG size or media type is invalid")
	}
	decoder := xml.NewDecoder(bytes.NewReader(data))
	rootSeen := false
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("parse SVG: %w", err)
		}
		element, ok := token.(xml.StartElement)
		if !ok {
			continue
		}
		if !rootSeen {
			if element.Name.Local != "svg" {
				return errors.New("asset root must be svg")
			}
			rootSeen = true
		}
		if element.Name.Local == "script" || element.Name.Local == "foreignObject" {
			return fmt.Errorf("unsafe SVG element %q", element.Name.Local)
		}
		for _, attribute := range element.Attr {
			name := strings.ToLower(attribute.Name.Local)
			value := strings.ToLower(strings.TrimSpace(attribute.Value))
			if strings.HasPrefix(name, "on") || name == "href" || strings.HasPrefix(value, "javascript:") || strings.HasPrefix(value, "data:") {
				return fmt.Errorf("unsafe SVG attribute %q", attribute.Name.Local)
			}
		}
	}
	if !rootSeen {
		return errors.New("SVG root is missing")
	}
	return nil
}
