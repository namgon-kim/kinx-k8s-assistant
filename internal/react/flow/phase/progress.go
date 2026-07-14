package phase

func MatchesCurrent(currentIndex, completedIndex int) bool {
	return currentIndex > 0 && completedIndex == currentIndex
}
