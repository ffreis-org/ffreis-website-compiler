package buildcmd

import (
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func readAsset(srcRoot, ref string) ([]byte, string, error) {
	cleanRef := strings.TrimPrefix(ref, "/")
	fullPath := filepath.Clean(filepath.Join(srcRoot, filepath.FromSlash(cleanRef)))
	if !strings.HasPrefix(fullPath, filepath.Clean(srcRoot)+string(filepath.Separator)) && fullPath != filepath.Clean(srcRoot) {
		return nil, "", fmt.Errorf("asset path escapes source root: %s", ref)
	}
	realPath, err := filepath.EvalSymlinks(fullPath)
	if err != nil {
		return nil, "", fmt.Errorf("resolving asset path %s: %w", ref, err)
	}
	realRoot, err := filepath.EvalSymlinks(filepath.Clean(srcRoot))
	if err != nil {
		return nil, "", fmt.Errorf("resolving source root: %w", err)
	}
	if !strings.HasPrefix(realPath, realRoot+string(filepath.Separator)) && realPath != realRoot {
		return nil, "", fmt.Errorf("asset path escapes source root via symlink: %s", ref)
	}
	content, err := os.ReadFile(realPath)
	if err != nil {
		return nil, "", fmt.Errorf("reading asset %s: %w", ref, err)
	}
	return content, cleanRef, nil
}

func assetToDataURL(srcRoot, ref string) (string, error) {
	content, p, err := readAsset(srcRoot, ref)
	if err != nil {
		return "", err
	}
	mimeType := detectMimeType(p, content)
	return "data:" + mimeType + ";base64," + encodeBase64(content), nil
}

func detectMimeType(p string, content []byte) string {
	ext := strings.ToLower(filepath.Ext(p))
	switch ext {
	case ".css":
		return mimeTextCSS
	case ".js":
		return "application/javascript"
	case ".svg":
		return "image/svg+xml"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".woff":
		return "font/woff"
	case extWoff2:
		return "font/woff2"
	case ".ttf":
		return "font/ttf"
	case ".ico":
		return "image/x-icon"
	default:
		return http.DetectContentType(content)
	}
}

func copyStaticAssets(srcRoot, dstRoot string) error {
	// css, fonts, images, and js are written directly as fingerprinted copies by
	// writeHashedAssets; copying the originals would produce unreferenced dead files.
	// ld/ (JSON-LD structured data) is served verbatim and is not fingerprinted.
	dirs := []string{"ld"}
	files := []string{"favicon.ico", "robots.txt", sitemapXML}

	if err := copyExistingDirs(srcRoot, dstRoot, dirs); err != nil {
		return err
	}
	return copyExistingFiles(srcRoot, dstRoot, files)
}

func copyExistingDirs(srcRoot, dstRoot string, dirs []string) error {
	for _, dir := range dirs {
		src := filepath.Join(srcRoot, dir)
		if _, err := os.Stat(src); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}

		dst := filepath.Join(dstRoot, dir)
		if err := copyDir(src, dst); err != nil {
			return err
		}
	}
	return nil
}

func copyExistingFiles(srcRoot, dstRoot string, files []string) error {
	for _, file := range files {
		src := filepath.Join(srcRoot, file)
		if _, err := os.Stat(src); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}

		dst := filepath.Join(dstRoot, file)
		if err := copyFile(src, dst); err != nil {
			return err
		}
	}
	return nil
}

func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)

		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}

		return copyFile(path, target)
	})
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}

	out, err := os.Create(dst)
	if err != nil {
		return err
	}

	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}

	return out.Close()
}

// extensionFromContentType maps a MIME content-type to a file extension.
func extensionFromContentType(contentType string) string {
	trimmed := strings.TrimSpace(strings.Split(contentType, ";")[0])
	if trimmed == "" {
		return ""
	}
	if exts, err := mime.ExtensionsByType(trimmed); err == nil {
		for _, ext := range exts {
			normalized := normalizeExt(ext)
			if normalized != "" {
				return normalized
			}
		}
	}
	switch trimmed {
	case mimeTextCSS:
		return ".css"
	case "application/javascript", "text/javascript":
		return ".js"
	case "font/woff2":
		return extWoff2
	case "font/woff":
		return ".woff"
	case "font/ttf", "application/x-font-ttf":
		return ".ttf"
	case "image/svg+xml":
		return ".svg"
	case "image/x-icon":
		return ".ico"
	default:
		return ""
	}
}
