// Code generated by "stringer -type=accessPointType"; DO NOT EDIT.

package main

import "strconv"

func _() {
	// An "invalid array index" compiler error signifies that the constant values have changed.
	// Re-run the stringer command to generate them again.
	var x [1]struct{}
	_ = x[typeUnknown-0]
	_ = x[typeLinksys-1]
	_ = x[typeVividHosting-2]
}

const _accessPointType_name = "typeUnknowntypeLinksystypeVividHosting"

var _accessPointType_index = [...]uint8{0, 11, 22, 38}

func (i accessPointType) String() string {
	if i < 0 || i >= accessPointType(len(_accessPointType_index)-1) {
		return "accessPointType(" + strconv.FormatInt(int64(i), 10) + ")"
	}
	return _accessPointType_name[_accessPointType_index[i]:_accessPointType_index[i+1]]
}
