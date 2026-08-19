package review

// Point 是一个审查要点，由规划阶段产出。
type Point struct {
	Desc string   `json:"desc"`
	Kw   []string `json:"kw"`
}
