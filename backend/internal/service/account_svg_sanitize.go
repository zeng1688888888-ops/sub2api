package service

import (
	"regexp"
	"strings"
)

var probeSVGTags = wordSet("svg g path rect circle ellipse line polyline polygon text tspan defs linearGradient radialGradient stop clipPath mask title desc pattern use symbol filter feGaussianBlur feOffset feBlend feColorMatrix feMerge feMergeNode feDropShadow feComposite feFlood")
var probeSVGAttributes = wordSet("id viewBox width height x y x1 x2 y1 y2 cx cy r rx ry d points fill fill-opacity fill-rule stroke stroke-width stroke-linecap stroke-linejoin stroke-dasharray stroke-dashoffset stroke-opacity opacity transform font-family font-size font-weight font-style letter-spacing word-spacing text-anchor dominant-baseline alignment-baseline dx dy rotate textLength lengthAdjust offset stop-color stop-opacity gradientUnits gradientTransform spreadMethod fx fy fr clip-path clip-rule mask maskUnits maskContentUnits patternUnits patternContentUnits patternTransform preserveAspectRatio vector-effect filter filterUnits primitiveUnits in in2 result stdDeviation mode type values operator k1 k2 k3 k4 flood-color flood-opacity color-interpolation-filters href")
var probeSVGRef = regexp.MustCompile(`^#[A-Za-z_][A-Za-z0-9_.:-]*$`)
var probeSVGPaintRef = regexp.MustCompile(`^url\(#[A-Za-z_][A-Za-z0-9_.:-]*\)$`)

func wordSet(value string) map[string]bool {
	m := map[string]bool{}
	for _, v := range strings.Fields(value) {
		m[v] = true
	}
	return m
}
