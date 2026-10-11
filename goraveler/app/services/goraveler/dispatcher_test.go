package goraveler

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	testifymock "github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/goravel/framework/contracts/process"
	mocksprocess "github.com/goravel/framework/mocks/process"
	"github.com/goravel/framework/testing/mock"
)

// scrubbedEnvArgs returns the `env -i PATH=... HOME=...` prefix the dispatcher
// uses to strip secrets from the agent subprocess.
func scrubbedEnvArgs() []any {
	return []any{"env", "-i", "PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")}
}

func TestOpencodeDispatcherRun(t *testing.T) {
	processMock := mock.Factory().Process()
	result := mocksprocess.NewResult(t)

	args := append(scrubbedEnvArgs(), "opencode", "run", "--agent", "review", "--", "do it")

	processMock.EXPECT().WithContext(testifymock.Anything).Return(processMock).Once()
	processMock.EXPECT().Path("/tmp/ws/goravel/goravel").Return(processMock).Once()
	processMock.EXPECT().Timeout(opencodeTimeout).Return(processMock).Once()
	processMock.EXPECT().DisableBuffering().Return(processMock).Once()
	processMock.EXPECT().Quietly().Return(processMock).Once()
	processMock.EXPECT().OnOutput(testifymock.Anything).Return(processMock).Once()
	processMock.EXPECT().Run(args[0].(string), args[1:]...).Return(result).Once()
	result.EXPECT().Failed().Return(false).Once()

	dispatcher := newOpencodeDispatcher()
	require.NoError(t, dispatcher.Run(context.Background(), dispatchRequest{
		dir:    "/tmp/ws/goravel/goravel",
		agent:  "review",
		prompt: "do it",
	}))

	processMock.AssertExpectations(t)
	result.AssertExpectations(t)
}

func TestOpencodeDispatcherRunFailure(t *testing.T) {
	processMock := mock.Factory().Process()
	result := mocksprocess.NewResult(t)

	args := append(scrubbedEnvArgs(), "opencode", "run", "--agent", "plan", "--", "do it")

	processMock.EXPECT().WithContext(testifymock.Anything).Return(processMock).Once()
	processMock.EXPECT().Path("/tmp/ws/goravel/framework").Return(processMock).Once()
	processMock.EXPECT().Timeout(opencodeTimeout).Return(processMock).Once()
	processMock.EXPECT().DisableBuffering().Return(processMock).Once()
	processMock.EXPECT().Quietly().Return(processMock).Once()
	// The dispatcher captures stderr via OnOutput; simulate a line arriving.
	processMock.EXPECT().OnOutput(testifymock.Anything).
		Run(func(handler process.OnOutputFunc) {
			handler(process.OutputTypeStdout, []byte("ignored"))
			handler(process.OutputTypeStderr, []byte("boom"))
		}).
		Return(processMock).
		Once()
	processMock.EXPECT().Run(args[0].(string), args[1:]...).Return(result).Once()
	result.EXPECT().Failed().Return(true).Once()
	result.EXPECT().ExitCode().Return(2).Once()

	dispatcher := newOpencodeDispatcher()
	err := dispatcher.Run(context.Background(), dispatchRequest{
		dir:    "/tmp/ws/goravel/framework",
		agent:  "plan",
		prompt: "do it",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), `"boom"`)

	processMock.AssertExpectations(t)
	result.AssertExpectations(t)
}

func TestOpencodeDispatcherRunStartError(t *testing.T) {
	processMock := mock.Factory().Process()
	result := mocksprocess.NewResult(t)

	args := append(scrubbedEnvArgs(), "opencode", "run", "--agent", "review", "--", "do it")

	processMock.EXPECT().WithContext(testifymock.Anything).Return(processMock).Once()
	processMock.EXPECT().Path("/tmp/ws/goravel/goravel").Return(processMock).Once()
	processMock.EXPECT().Timeout(opencodeTimeout).Return(processMock).Once()
	processMock.EXPECT().DisableBuffering().Return(processMock).Once()
	processMock.EXPECT().Quietly().Return(processMock).Once()
	// No stderr captured, so the start error is reported instead.
	processMock.EXPECT().OnOutput(testifymock.Anything).Return(processMock).Once()
	processMock.EXPECT().Run(args[0].(string), args[1:]...).Return(result).Once()
	result.EXPECT().Failed().Return(true).Once()
	result.EXPECT().ExitCode().Return(-1).Once()
	result.EXPECT().Error().Return(errors.New("start failed")).Once()

	dispatcher := newOpencodeDispatcher()
	err := dispatcher.Run(context.Background(), dispatchRequest{
		dir:    "/tmp/ws/goravel/goravel",
		agent:  "review",
		prompt: "do it",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "start failed")

	processMock.AssertExpectations(t)
	result.AssertExpectations(t)
}

func TestOpencodeDispatcherDefaults(t *testing.T) {
	dispatcher := newOpencodeDispatcher()
	require.Equal(t, opencodeBin, dispatcher.bin)
	require.Equal(t, 30*time.Minute, dispatcher.deadline)
}
