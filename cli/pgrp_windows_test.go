package main

// processGroup has nothing to compare on Windows, where a process group is a
// creation flag and not something a process can read back about itself.
func processGroup() int { return 0 }
