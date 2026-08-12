package relay

import (
	"fmt"
	"net/http"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relay/channel/opencodego"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
)

func validateOpenCodeGoPassThrough(info *relaycommon.RelayInfo, incoming types.RelayFormat, enabled bool) *types.NewAPIError {
	if !enabled || info.ChannelType != constant.ChannelTypeOpenCodeGo || opencodego.PassThroughCompatible(info, incoming) {
		return nil
	}
	return types.NewErrorWithStatusCode(
		fmt.Errorf("OpenCodeGo cannot pass through a %s request to the selected model protocol; disable pass-through body conversion", incoming),
		types.ErrorCodeInvalidRequest,
		http.StatusBadRequest,
		types.ErrOptionWithSkipRetry(),
	)
}
