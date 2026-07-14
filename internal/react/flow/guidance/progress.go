package guidance

func MarkCompleted(completed, skipped map[int]bool, step, total int) (map[int]bool, bool, bool) {
	result := make(map[int]bool, len(completed)+1)
	for index, value := range completed {
		result[index] = value
	}
	if step <= 0 || step > total || result[step] || skipped[step] {
		return result, false, false
	}
	result[step] = true
	for index := 1; index <= total; index++ {
		if !result[index] && !skipped[index] {
			return result, true, false
		}
	}
	return result, true, total > 0
}
