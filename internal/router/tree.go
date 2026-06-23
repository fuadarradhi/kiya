package router

import (
	"path"
	"regexp"
	"strings"
	"sync"

	"github.com/fuadarradhi/kiya/internal/logger"
)

type HandlerFunc func(c any) error

type Middleware func(HandlerFunc) HandlerFunc

type nodeType uint8

const (
	static nodeType = iota
	paramNode
	regexNode
	wildcardNode
)

type node struct {
	part      string
	nType     nodeType
	paramName string
	regex     *regexp.Regexp
	children  []*node
	handler   HandlerFunc
}

type Param struct {
	Key   string
	Value string
}

type Tree struct {
	roots map[string]*node
	mu    sync.RWMutex
}

func NewTree() *Tree {
	return &Tree{
		roots: make(map[string]*node),
	}
}

func (t *Tree) AddRoute(method, path string, h HandlerFunc) {
	t.mu.Lock()
	defer t.mu.Unlock()

	fullPath := cleanPath(path)

	if t.roots[method] == nil {
		t.roots[method] = &node{}
	}

	current := t.roots[method]
	segments := splitPath(fullPath)

	if len(segments) == 0 {
		current.handler = h
		return
	}

	for i, seg := range segments {
		n := parseSegment(seg)

		var child *node
		for _, c := range current.children {
			if sameNode(c, n) {
				child = c
				break
			}
		}

		if child == nil {
			for _, c := range current.children {
				isConflict := false
				if c.nType == paramNode && (n.nType == paramNode || n.nType == regexNode) {
					isConflict = true
				}
				if (c.nType == regexNode || c.nType == wildcardNode) && (n.nType == paramNode || n.nType == regexNode) {
					isConflict = true
				}
				if c.nType == wildcardNode && n.nType == wildcardNode {
					isConflict = true
				}

				if isConflict {
					logger.LogError("ROUTE CONFLICT: Cannot register '%s'. Segment '%s' conflicts with existing '%s'.", fullPath, seg, c.part)
					return
				}
			}

			child = n
			current.children = append(current.children, child)
		}

		current = child

		if n.nType == wildcardNode {
			current.handler = h
			return
		}

		if i == len(segments)-1 {
			current.handler = h
		}
	}
}

func (t *Tree) FindRoute(method, path string) (HandlerFunc, []Param) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	root := t.roots[method]
	if root == nil {
		return nil, nil
	}

	segments := splitPath(cleanPath(path))
	var params []Param

	h := t.search(root, segments, &params)
	return h, params
}

func (t *Tree) search(n *node, segments []string, params *[]Param) HandlerFunc {
	if len(segments) == 0 {
		return n.handler
	}

	seg := segments[0]
	rest := segments[1:]

	for _, c := range n.children {
		if c.nType == static && c.part == seg {
			if h := t.search(c, rest, params); h != nil {
				return h
			}
		}
	}

	for _, c := range n.children {
		if c.nType == regexNode && c.regex.MatchString(seg) {
			*params = append(*params, Param{c.paramName, seg})
			if h := t.search(c, rest, params); h != nil {
				return h
			}
			*params = (*params)[:len(*params)-1]
		}
	}

	for _, c := range n.children {
		if c.nType == paramNode {
			*params = append(*params, Param{c.paramName, seg})
			if h := t.search(c, rest, params); h != nil {
				return h
			}
			*params = (*params)[:len(*params)-1]
		}
	}

	for _, c := range n.children {
		if c.nType == wildcardNode {
			val := strings.Join(segments, "/")
			*params = append(*params, Param{c.paramName, val})
			return c.handler
		}
	}

	return nil
}

func (t *Tree) AnyMethodExists(path string) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()

	for _, root := range t.roots {
		if root != nil {
			if h, _ := t.findRouteInRoot(root, path); h != nil {
				return true
			}
		}
	}
	return false
}

func (t *Tree) findRouteInRoot(root *node, path string) (HandlerFunc, []Param) {
	segments := splitPath(cleanPath(path))
	var params []Param
	h := t.search(root, segments, &params)
	return h, params
}

func parseSegment(seg string) *node {
	if !strings.HasPrefix(seg, "{") {
		return &node{part: seg, nType: static}
	}

	body := seg[1 : len(seg)-1]

	if len(body) > 100 {
		return &node{nType: paramNode, paramName: body}
	}

	parts := strings.SplitN(body, ":", 2)
	name := parts[0]

	if len(parts) == 2 {
		pattern := parts[1]
		if pattern == "*" {
			return &node{nType: wildcardNode, paramName: name}
		}
		re, err := regexp.Compile("^" + pattern + "$")
		if err == nil {
			return &node{nType: regexNode, paramName: name, regex: re}
		}
	}
	return &node{nType: paramNode, paramName: name}
}

func sameNode(a, b *node) bool {
	return a.nType == b.nType && a.part == b.part && a.paramName == b.paramName
}

func splitPath(p string) []string {
	if p == "/" {
		return nil
	}
	p = strings.Trim(p, "/")
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

func cleanPath(p string) string {
	if p == "" {
		return "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return path.Clean(p)
}
