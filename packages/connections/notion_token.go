package connections

type notionTokenBundle = clickStackTokenBundle

var (
	parseNotionTokenBundle  = parseClickStackTokenBundle
	encodeNotionTokenBundle = encodeClickStackTokenBundle
	notionExpiresAt         = clickStackExpiresAt
)
