package run

// Live reports whether a process is running the Run whose private
// directory is dir.
func Live(dir string) bool { return alive(dir) }
