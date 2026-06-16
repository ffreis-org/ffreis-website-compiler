package sitegen

import (
	"fmt"
	"io"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
)

type tracedMap map[string]any
type tracedSlice []any

type accessTracer struct {
	mu         sync.Mutex
	used       map[string]struct{}
	registered []uintptr
}

type traceMetadata struct {
	path   string
	tracer *accessTracer
}

var traceRegistry sync.Map

func TraceSiteDataUsage(pages []PageTemplate, siteData map[string]any) ([]string, error) {
	tracer := &accessTracer{
		used: make(map[string]struct{}),
	}
	defer tracer.cleanup()
	tracedData := wrapTracedValue(siteData, tracer, "")

	for _, page := range pages {
		if err := page.Tmpl.ExecuteTemplate(io.Discard, "layout", NewTemplateData(page.Name, tracedData)); err != nil {
			return nil, fmt.Errorf("rendering %s for site data trace: %w", page.Name, err)
		}
	}
	return tracer.usedPaths(), nil
}

func NewTemplateData(pageName string, siteData any) map[string]any {
	if siteData == nil {
		siteData = map[string]any{}
	}
	return map[string]any{
		"PageName": pageName,
		"SiteData": siteData,
	}
}

func dig(root any, keys ...any) (any, error) {
	current := root
	tracePath, tracer := traceStateForValue(current)

	for _, key := range keys {
		if current == nil {
			return nil, nil
		}

		next, nextTracePath, err := descend(current, key, tracePath, tracer)
		if err != nil {
			return nil, err
		}
		if next == nil && current != nil {
			// For missing keys/out-of-range indexes, preserve original behavior (nil, nil).
			return nil, nil
		}

		current = next
		tracePath = nextTracePath
		tracePath, tracer = updateTraceStateFromMetadata(current, tracePath, tracer)
	}

	recordTraceIfNeeded(tracer, tracePath, current)
	return current, nil
}

func traceStateForValue(value any) (string, *accessTracer) {
	if metadata, ok := traceMetadataForValue(value); ok {
		return metadata.path, metadata.tracer
	}
	return "", nil
}

func updateTraceStateFromMetadata(value any, tracePath string, tracer *accessTracer) (string, *accessTracer) {
	if nextMetadata, ok := traceMetadataForValue(value); ok {
		return nextMetadata.path, nextMetadata.tracer
	}
	return tracePath, tracer
}

func recordTraceIfNeeded(tracer *accessTracer, tracePath string, value any) {
	if tracer == nil {
		return
	}
	if tracePath == "" {
		return
	}
	if !shouldTraceValue(value) {
		return
	}
	tracer.record(tracePath)
}

func descend(current any, key any, tracePath string, tracer *accessTracer) (any, string, error) {
	switch typed := current.(type) {
	case map[string]any:
		return descendMapLike(typed, key, tracePath, tracer)
	case tracedMap:
		return descendMapLike(map[string]any(typed), key, tracePath, tracer)
	case []any:
		return descendSliceLike(typed, key, tracePath, tracer)
	case tracedSlice:
		return descendSliceLike([]any(typed), key, tracePath, tracer)
	default:
		return nil, tracePath, fmt.Errorf("dig cannot descend into %T", current)
	}
}

func descendMapLike(m map[string]any, key any, tracePath string, tracer *accessTracer) (any, string, error) {
	keyString, err := stringifyKey(key)
	if err != nil {
		return nil, tracePath, err
	}
	next := m[keyString]
	if tracer != nil {
		tracePath = joinTracePath(tracePath, keyString)
	}
	return next, tracePath, nil
}

func descendSliceLike(s []any, key any, tracePath string, tracer *accessTracer) (any, string, error) {
	index, err := integerKey(key)
	if err != nil {
		return nil, tracePath, err
	}
	if index < 0 || index >= len(s) {
		return nil, tracePath, nil
	}
	next := s[index]
	if tracer != nil {
		tracePath = joinTracePath(tracePath, strconv.Itoa(index))
	}
	return next, tracePath, nil
}

func wrapTracedValue(value any, tracer *accessTracer, path string) any {
	switch typed := value.(type) {
	case map[string]any:
		wrapped := make(tracedMap, len(typed))
		for key, item := range typed {
			wrapped[key] = wrapTracedValue(item, tracer, joinTracePath(path, key))
		}
		registerTraceMetadata(wrapped, traceMetadata{path: path, tracer: tracer})
		return wrapped
	case []any:
		wrapped := make(tracedSlice, len(typed))
		for i, item := range typed {
			wrapped[i] = wrapTracedValue(item, tracer, joinTracePath(path, strconv.Itoa(i)))
		}
		registerTraceMetadata(wrapped, traceMetadata{path: path, tracer: tracer})
		return wrapped
	default:
		return value
	}
}

func registerTraceMetadata(value any, metadata traceMetadata) {
	pointer, ok := compositePointer(value)
	if !ok || pointer == 0 {
		return
	}
	traceRegistry.Store(pointer, metadata)
	if metadata.tracer != nil {
		metadata.tracer.mu.Lock()
		metadata.tracer.registered = append(metadata.tracer.registered, pointer)
		metadata.tracer.mu.Unlock()
	}
}

func traceMetadataForValue(value any) (traceMetadata, bool) {
	pointer, ok := compositePointer(value)
	if !ok || pointer == 0 {
		return traceMetadata{}, false
	}
	metadata, ok := traceRegistry.Load(pointer)
	if !ok {
		return traceMetadata{}, false
	}
	typed, ok := metadata.(traceMetadata)
	return typed, ok
}

func compositePointer(value any) (uintptr, bool) {
	rv := reflect.ValueOf(value)
	switch rv.Kind() {
	case reflect.Map, reflect.Slice:
		return rv.Pointer(), true
	default:
		return 0, false
	}
}

func shouldTraceValue(value any) bool {
	switch value.(type) {
	case tracedMap, map[string]any:
		return false
	default:
		return true
	}
}

func joinTracePath(prefix, segment string) string {
	if strings.TrimSpace(prefix) == "" {
		return segment
	}
	return prefix + "." + segment
}

func stringifyKey(key any) (string, error) {
	switch typed := key.(type) {
	case string:
		return typed, nil
	case fmt.Stringer:
		return typed.String(), nil
	default:
		return "", fmt.Errorf("dig map keys must be strings, got %T", key)
	}
}

func integerKey(key any) (int, error) {
	switch typed := key.(type) {
	case int:
		return typed, nil
	case int8:
		return int(typed), nil
	case int16:
		return int(typed), nil
	case int32:
		return int(typed), nil
	case int64:
		return int(typed), nil
	case uint:
		return int(typed), nil //nolint:gosec
	case uint8:
		return int(typed), nil
	case uint16:
		return int(typed), nil
	case uint32:
		return int(typed), nil
	case uint64:
		return int(typed), nil //nolint:gosec
	case string:
		index, err := strconv.Atoi(typed)
		if err != nil {
			return 0, fmt.Errorf("dig slice keys must be integers, got %q", typed)
		}
		return index, nil
	default:
		return 0, fmt.Errorf("dig slice keys must be integers, got %T", key)
	}
}

func (t *accessTracer) record(path string) {
	if strings.TrimSpace(path) == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.used[path] = struct{}{}
}

func (t *accessTracer) usedPaths() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	paths := make([]string, 0, len(t.used))
	for path := range t.used {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

func (t *accessTracer) cleanup() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, pointer := range t.registered {
		traceRegistry.Delete(pointer)
	}
	t.registered = nil
}
