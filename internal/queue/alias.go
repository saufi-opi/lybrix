package queue

// Cmdable2 aliases the go-redis Cmdable so worker handlers can hold a
// redis client without importing go-redis in every file.
type Cmdable2 = cmdableReal
