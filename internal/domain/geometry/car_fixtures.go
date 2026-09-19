package geometry

// CarFixturesForTest are the cars the template's fences build, in millimetres: a
// default sports car and the extremes of what the template accepts. Named for what
// it is, like GearOutlineForTest; the kernel fences in internal/domain/cad build
// the same cars through OpenCASCADE.
func CarFixturesForTest() map[string]map[string]float64 {
	return map[string]map[string]float64{
		// Only the four required numbers: everything else from the sports cars.
		"default": {"length": 4500, "width": 1900, "height": 1250, "wheelbase": 2600},
		// Long, low and wide, big wheels, cab-forward, soft edges.
		"long-low-hypercar": {"length": 5200, "width": 2150, "height": 1000, "wheelbase": 2950,
			"wheel_diameter": 760, "ride_height": 60, "track_front": 1850, "track_rear": 1800,
			"tyre_width": 320, "cabin_start": 0.22, "cabin_length": 0.36, "nose_height": 0.3,
			"tail_height": 0.85, "edge_radius": 0.06, "lug_count": 10, "diffuser_fins": 9},
		// Short and tall on big tyres, high ride, boxy, crisp.
		"short-tall-suv": {"length": 3900, "width": 1850, "height": 1950, "wheelbase": 2350,
			"wheel_diameter": 820, "ride_height": 260, "front_overhang": 780, "cabin_start": 0.2,
			"cabin_length": 0.6, "nose_height": 0.55, "belt_height": 0.55, "tail_height": 0.96,
			"roof_width": 0.92, "edge_radius": 0.005, "lug_count": 3, "diffuser_fins": 2},
		// A long-hood GT: the cabin far back, a fastback tail.
		"long-hood-gt": {"length": 4900, "width": 1950, "height": 1300, "wheelbase": 2850,
			"cabin_start": 0.5, "cabin_length": 0.36, "tail_height": 0.6, "roof_width": 0.45},
	}
}

// CarDocumentForTest is fixture name written out and bound, as a turn would store
// it: the document the kernel fences build.
func CarDocumentForTest(name string) Document {
	d := Document{Name: name, Units: "mm", Parts: []Part{{ID: "gt", Name: "GT", Shape: "car",
		Class: "sports", Size: CarFixturesForTest()[name], Position: []float64{0, 0, 0},
		Rotation: []float64{0, 0, 0}}}}
	ExpandTemplates(&d)
	d.Bind()
	return d
}
