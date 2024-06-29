// Code generated by "stringer -type=station"; DO NOT EDIT.

package radio

import "strconv"

func _() {
	// An "invalid array index" compiler error signifies that the constant values have changed.
	// Re-run the stringer command to generate them again.
	var x [1]struct{}
	_ = x[red1-0]
	_ = x[red2-1]
	_ = x[red3-2]
	_ = x[blue1-3]
	_ = x[blue2-4]
	_ = x[blue3-5]
}

const _station_name = "red1red2red3blue1blue2blue3"

var _station_index = [...]uint8{0, 4, 8, 12, 17, 22, 27}

func (i station) String() string {
	if i < 0 || i >= station(len(_station_index)-1) {
		return "station(" + strconv.FormatInt(int64(i), 10) + ")"
	}
	return _station_name[_station_index[i]:_station_index[i+1]]
}
