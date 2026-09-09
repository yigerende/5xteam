package httpapi

import "time"

// Beijing time is used for human-facing names and export filenames. Persisted
// timestamps remain UTC so ordering and cross-server comparisons stay stable.
var beijingLocation = time.FixedZone("Asia/Shanghai", 8*60*60)

func beijingNow() time.Time { return time.Now().In(beijingLocation) }
