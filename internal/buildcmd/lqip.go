package buildcmd

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	_ "image/png" // register PNG decoder for image.Decode
	"strings"

	_ "golang.org/x/image/webp" // register WebP decoder for image.Decode
)

const lqipThumbSize = 20 // pixels wide

// lqipCSS and lqipScript are injected once per page that has LQIP images.
// The class approach avoids inline-style specificity fights with existing CSS.
const lqipCSS = `<style>.lqip-pending{filter:blur(8px);transition:filter .3s ease}</style>`

// The script swaps in the full image (from data-src) once it's loaded.
// Using a helper Image to load in the background; swaps src and removes the
// blur class atomically so there's no visible flash of the pixelated thumbnail.
const lqipScript = `<script>(function(){` +
	`document.querySelectorAll("img[data-src]").forEach(function(img){` +
	`var s=new Image();` +
	`s.onload=function(){img.src=s.src;img.classList.remove("lqip-pending")};` +
	`s.onerror=function(){img.src=img.dataset.src;img.classList.remove("lqip-pending")};` +
	`s.src=img.dataset.src;` +
	`if(s.complete&&s.naturalWidth>0){img.src=s.src;img.classList.remove("lqip-pending")}` +
	`})})();</script>`

// processLQIPImages finds local raster images with loading="eager", generates a
// tiny blurry placeholder (LQIP), and rewrites each img tag to show the blur-up
// while the full image loads in the background via a small injected script.
//
// SVGs, external URLs, data URIs, and images that fail to decode are left unchanged.
func processLQIPImages(html, assetsDir string) (string, error) {
	hasLQIP := false

	var err error
	html, err = replaceTagWith(html, imgTagRE, func(tag string, refs []string) (string, error) {
		src := refs[1]
		if getTagAttr(tag, "loading") != "eager" {
			return tag, nil
		}
		if isExternalRef(src) || isDataURI(src) || isSVGPath(src) {
			return tag, nil
		}

		data, _, readErr := readAsset(assetsDir, src)
		if readErr != nil {
			return tag, nil // not found or unreadable — skip
		}
		lqipURI, genErr := generateLQIP(data)
		if genErr != nil {
			return tag, nil // unsupported format or decode failure — skip
		}

		hasLQIP = true
		return buildLQIPTag(tag, src, lqipURI), nil
	})
	if err != nil {
		return "", err
	}

	if !hasLQIP {
		return html, nil
	}

	html = strings.Replace(html, headEndTag, lqipCSS+headEndReplacement, 1)
	if strings.Contains(html, "</body>") {
		html = strings.Replace(html, "</body>", lqipScript+"\n</body>", 1)
	} else {
		html = strings.Replace(html, "</html>", lqipScript+"\n</html>", 1)
	}
	return html, nil
}

// generateLQIP decodes data as a raster image, scales it to lqipThumbSize wide,
// and returns a base64-encoded JPEG data URI for use as a blur-up placeholder.
func generateLQIP(data []byte) (string, error) {
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("decoding image: %w", err)
	}

	sb := src.Bounds()
	srcW, srcH := sb.Dx(), sb.Dy()
	if srcW == 0 || srcH == 0 {
		return "", fmt.Errorf("zero-dimension image")
	}

	w := lqipThumbSize
	h := (srcH * w) / srcW
	if h < 1 {
		h = 1
	}

	thumb := scaleNearest(src, w, h)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, thumb, &jpeg.Options{Quality: 20}); err != nil {
		return "", fmt.Errorf("encoding LQIP JPEG: %w", err)
	}

	return "data:image/jpeg;base64," + encodeBase64(buf.Bytes()), nil
}

// scaleNearest returns a new RGBA image scaled to (w, h) using nearest-neighbour.
// For a 20px LQIP (which will be rendered blurred), this quality is sufficient.
func scaleNearest(src image.Image, w, h int) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	sb := src.Bounds()
	srcW, srcH := sb.Dx(), sb.Dy()
	for y := range h {
		sy := sb.Min.Y + y*srcH/h
		for x := range w {
			sx := sb.Min.X + x*srcW/w
			r, g, b, a := src.At(sx, sy).RGBA()
			// r,g,b,a are 16-bit (0–0xffff); >>8 gives 0–0xff, safe to cast.
			dst.SetRGBA(x, y, color.RGBA{
				R: uint8(r >> 8), //nolint:gosec // G115: safe, see comment above
				G: uint8(g >> 8), //nolint:gosec // G115: safe, see comment above
				B: uint8(b >> 8), //nolint:gosec // G115: safe, see comment above
				A: uint8(a >> 8), //nolint:gosec // G115: safe, see comment above
			})
		}
	}
	return dst
}

// buildLQIPTag rewrites an img tag to use the LQIP data URI as src, moving the
// original src to data-src, and adds the lqip-pending class for the blur filter.
func buildLQIPTag(tag, originalSrc, lqipURI string) string {
	// Replace src="original" with src="lqip" data-src="original"
	for _, q := range []string{`"`, `'`} {
		placeholder := `src=` + q + originalSrc + q
		if strings.Contains(tag, placeholder) {
			tag = strings.Replace(tag, placeholder,
				`src=`+q+lqipURI+q+` data-src=`+q+originalSrc+q, 1)
			break
		}
	}

	// Add lqip-pending to existing class or insert new class attribute
	for _, q := range []string{`"`, `'`} {
		prefix := `class=` + q
		if idx := strings.Index(tag, prefix); idx != -1 {
			end := idx + len(prefix)
			closeIdx := strings.Index(tag[end:], q)
			if closeIdx >= 0 {
				existing := tag[end : end+closeIdx]
				newClass := strings.TrimSpace(existing + " lqip-pending")
				tag = tag[:end] + newClass + tag[end+closeIdx:]
				return tag
			}
		}
	}
	return strings.Replace(tag, "<img ", `<img class="lqip-pending" `, 1)
}

// isSVGPath reports whether the path ends in .svg.
func isSVGPath(ref string) bool {
	return strings.HasSuffix(strings.ToLower(strings.TrimSpace(ref)), ".svg")
}
