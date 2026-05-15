package buildcmd

import (
	"strings"
	"testing"
)

func TestMinifyCSS_Empty(t *testing.T) {
	if got := minifyCSS(""); got != "" {
		t.Fatalf("expected empty string, got %q", got)
	}
}

func TestMinifyCSS_StripsBlockComments(t *testing.T) {
	input := "/* header comment */\nbody { color: red; /* inline comment */ }"
	got := minifyCSS(input)
	if strings.Contains(got, "/*") {
		t.Fatalf("expected block comments stripped, got %q", got)
	}
	if !strings.Contains(got, "color:red") {
		t.Fatalf("expected color rule preserved, got %q", got)
	}
}

func TestMinifyCSS_PreservesBangComments(t *testing.T) {
	input := "/*! MIT License */\nbody { color: red; }"
	got := minifyCSS(input)
	if !strings.Contains(got, "/*! MIT License */") {
		t.Fatalf("expected preserved comment kept, got %q", got)
	}
}

func TestMinifyCSS_CollapsesWhitespace(t *testing.T) {
	input := "body  {\n\tcolor:\n\t\tred;\n}\n\n.foo {\n\tmargin: 0;\n}"
	got := minifyCSS(input)
	if strings.Contains(got, "\n") {
		t.Fatalf("expected newlines collapsed, got %q", got)
	}
	if strings.Contains(got, "\t") {
		t.Fatalf("expected tabs collapsed, got %q", got)
	}
}

func TestMinifyCSS_StripsSpacesAroundStructChars(t *testing.T) {
	input := "body { color : red ; margin : 0 }"
	got := minifyCSS(input)
	if strings.Contains(got, " :") || strings.Contains(got, ": ") {
		t.Fatalf("expected spaces around : stripped, got %q", got)
	}
	if strings.Contains(got, " ;") {
		t.Fatalf("expected spaces before ; stripped, got %q", got)
	}
}

func TestMinifyCSS_RemovesTrailingSemicolonBeforeBrace(t *testing.T) {
	input := "body { color: red; margin: 0; }"
	got := minifyCSS(input)
	if strings.Contains(got, ";}") {
		t.Fatalf("expected trailing semicolon before } removed, got %q", got)
	}
}

func TestMinifyCSS_PreservesURLContent(t *testing.T) {
	input := `@font-face { src: url("fonts/inter.woff2"); }`
	got := minifyCSS(input)
	if !strings.Contains(got, `url("fonts/inter.woff2")`) {
		t.Fatalf("expected url() content preserved, got %q", got)
	}
}

func TestMinifyCSS_PreservesDataURIInURL(t *testing.T) {
	dataURI := `url("data:image/png;base64,iVBORw0KGgo=")`
	input := ".bg { background: " + dataURI + "; }"
	got := minifyCSS(input)
	if !strings.Contains(got, dataURI) {
		t.Fatalf("expected data URI in url() preserved, got %q", got)
	}
}

func TestMinifyCSS_PreservesCharset(t *testing.T) {
	input := `@charset "UTF-8";
body { color: red; }`
	got := minifyCSS(input)
	if !strings.Contains(got, `@charset "UTF-8"`) {
		t.Fatalf("expected @charset preserved, got %q", got)
	}
}

func TestMinifyCSS_MediaQueryPreservesStructure(t *testing.T) {
	input := `@media screen and (max-width: 768px) { body { font-size: 14px; } }`
	got := minifyCSS(input)
	if !strings.Contains(got, "@media") {
		t.Fatalf("expected @media rule preserved, got %q", got)
	}
	if !strings.Contains(got, "font-size:14px") {
		t.Fatalf("expected font-size rule preserved, got %q", got)
	}
}
