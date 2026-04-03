package cache

type Layer int

const (
	LayerImmutable Layer = iota
	LayerMutable
	LayerUncacheable
)

func Classify(method string) Layer {
	switch method {
	case "eth_getTransactionByHash",
		"eth_getTransactionReceipt",
		"eth_getBlockByHash",
		"eth_getUncleByBlockHashAndIndex":
		return LayerImmutable
	case "eth_blockNumber",
		"eth_gasPrice",
		"eth_getBalance",
		"eth_call",
		"eth_estimateGas",
		"eth_feeHistory":
		return LayerMutable
	default:
		return LayerUncacheable
	}
}
