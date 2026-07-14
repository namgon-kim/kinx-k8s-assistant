package guidance

func MarkCompleted(completed map[int]bool, step, total int) (map[int]bool, bool) {
	result := make(map[int]bool, len(completed)+1)
	for index, value := range completed {
		result[index] = value
	}
	if step > 0 && step <= total {
		result[step] = true
	}
	for index := 1; index <= total; index++ {
		if !result[index] {
			return result, false
		}
	}
	return result, total > 0
}
