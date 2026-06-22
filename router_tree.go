package kiya

import (
	"fmt"
	"path"
	"regexp"
	"strings"
)

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

func (r *Router) addRoute(method, path string, h HandlerFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()

	fullPath := cleanPath(r.prefix + path)

	if r.trees[method] == nil {
		r.trees[method] = &node{}
	}

	current := r.trees[method]
	segments := splitPath(fullPath)

	if len(segments) == 0 {
		current.handler = chain(h, r.middleware...)
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
					LogError("ROUTE CONFLICT: Cannot register '%s'. Segment '%s' conflicts with existing '%s'.", fullPath, seg, c.part)
					panic(fmt.Sprintf("route conflict: %s", fullPath))
				}
			}

			child = n
			current.children = append(current.children, child)
		}

		current = child

		if n.nType == wildcardNode {
			current.handler = chain(h, r.middleware...)
			return
		}

		if i == len(segments)-1 {
			current.handler = chain(h, r.middleware...)
		}
	}
}

func (r *Router) findRoute(root *node, path string) (HandlerFunc, []param) {
	if root == nil {
		return nil, nil
	}

	segments := splitPath(cleanPath(path))
	var params []param

	h := r.search(root, segments, &params)
	return h, params
}

func (r *Router) search(n *node, segments []string, params *[]param) HandlerFunc {
	if len(segments) == 0 {
		return n.handler
	}

	seg := segments[0]
	rest := segments[1:]

	for _, c := range n.children {
		if c.nType == static && c.part == seg {
			if h := r.search(c, rest, params); h != nil {
				return h
			}
		}
	}

	for _, c := range n.children {
		if c.nType == regexNode && c.regex.MatchString(seg) {
			*params = append(*params, param{c.paramName, seg})
			if h := r.search(c, rest, params); h != nil {
				return h
			}
			*params = (*params)[:len(*params)-1]
		}
	}

	for _, c := range n.children {
		if c.nType == paramNode {
			*params = append(*params, param{c.paramName, seg})
			if h := r.search(c, rest, params); h != nil {
				return h
			}
			*params = (*params)[:len(*params)-1]
		}
	}

	for _, c := range n.children {
		if c.nType == wildcardNode {
			val := strings.Join(segments, "/")
			*params = append(*params, param{c.paramName, val})
			return c.handler
		}
	}

	return nil
}

func (r *Router) anyMethodExists(path string) bool {
	for _, root := range r.trees {
		if root != nil {
			if h, _ := r.findRoute(root, path); h != nil {
				return true
			}
		}
	}
	return false
}

func chain(h HandlerFunc, m ...Middleware) HandlerFunc {
	if h == nil {
		return nil
	}

	next := h

	for i := len(m) - 1; i >= 0; i-- {
		mw := m[i]
		currentNext := next

		next = func(c *Resources) error {
			if c.aborted {
				return nil
			}
			return mw(currentNext)(c)
		}
	}

	return next
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
