package agent

import (
	"context"
	"testing"

	testifymock "github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	mocksprocess "github.com/goravel/framework/mocks/process"
	"github.com/goravel/framework/testing/mock"
)

// projectArgs returns the argv `paseo project ls --json` runs with, behind the
// `env -i PATH=... HOME=...` prefix. `-q` is absent on purpose: it prints ids
// only and cannot answer a name lookup.
func projectArgs() []any {
	return append(scrubbedEnvArgs(), paseoBin, "project", "ls", "--json")
}

// resolvedProject returns a resolver already holding an answer, so a test can
// dispatch without arming `paseo project ls`.
func resolvedProject(project Project) *projectResolver {
	return &projectResolver{resolved: true, project: project}
}

// expectProjectLookup arms the shared process mock for one project lookup and
// returns the result whose expectations the caller sets.
func expectProjectLookup(t *testing.T, processMock *mocksprocess.Process) *mocksprocess.Result {
	t.Helper()

	args := projectArgs()
	result := mocksprocess.NewResult(t)

	processMock.EXPECT().WithContext(testifymock.Anything).Return(processMock).Once()
	processMock.EXPECT().Timeout(projectLookupTimeout).Return(processMock).Once()
	processMock.EXPECT().Quietly().Return(processMock).Once()
	processMock.EXPECT().Run(args[0].(string), args[1:]...).Return(result).Once()

	return result
}

func TestResolveProject(t *testing.T) {
	tests := []struct {
		name    string
		lookup  func(result *mocksprocess.Result)
		want    Project
		wantErr string
	}{
		{
			name: "resolves the named project",
			lookup: func(result *mocksprocess.Result) {
				result.EXPECT().Failed().Return(false).Once()
				result.EXPECT().Output().
					Return(`[{"projectId":"prj_tmp","name":"tmp","kind":"non_git","path":"/tmp"}]`).
					Once()
			},
			want: Project{ID: "prj_tmp", Path: "/tmp"},
		},
		{
			name: "skips projects with another name",
			lookup: func(result *mocksprocess.Result) {
				result.EXPECT().Failed().Return(false).Once()
				result.EXPECT().Output().
					Return(`[{"projectId":"prj_other","name":"other","path":"/srv"},` +
						`{"projectId":"prj_tmp","name":"tmp","path":"/tmp"}]`).
					Once()
			},
			want: Project{ID: "prj_tmp", Path: "/tmp"},
		},
		{
			name: "unknown name",
			lookup: func(result *mocksprocess.Result) {
				result.EXPECT().Failed().Return(false).Once()
				result.EXPECT().Output().Return(`[]`).Once()
			},
			wantErr: `agent: AGENT_PROJECT_NAME="tmp": no paseo project with that name`,
		},
		{
			name: "failed lookup",
			lookup: func(result *mocksprocess.Result) {
				result.EXPECT().Failed().Return(true).Once()
				result.EXPECT().ExitCode().Return(1).Once()
				result.EXPECT().ErrorOutput().Return("daemon down").Once()
			},
			wantErr: `agent: AGENT_PROJECT_NAME="tmp": ` + "`paseo project ls --json`" +
				` failed (exit 1): daemon down`,
		},
		{
			name: "unreadable output",
			lookup: func(result *mocksprocess.Result) {
				result.EXPECT().Failed().Return(false).Once()
				result.EXPECT().Output().Return("not json").Once()
			},
			wantErr: `agent: AGENT_PROJECT_NAME="tmp": decode ` + "`paseo project ls --json`",
		},
		{
			name: "project without a path",
			lookup: func(result *mocksprocess.Result) {
				result.EXPECT().Failed().Return(false).Once()
				result.EXPECT().Output().Return(`[{"projectId":"prj_tmp","name":"tmp","path":""}]`).Once()
			},
			wantErr: `agent: AGENT_PROJECT_NAME="tmp": that paseo project has no id or path`,
		},
		{
			// A blank id would cache a project every later dispatch rejects, so it
			// is refused here as configuration instead.
			name: "project without an id",
			lookup: func(result *mocksprocess.Result) {
				result.EXPECT().Failed().Return(false).Once()
				result.EXPECT().Output().Return(`[{"projectId":"","name":"tmp","path":"/tmp"}]`).Once()
			},
			wantErr: `agent: AGENT_PROJECT_NAME="tmp": that paseo project has no id or path`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			processMock := mock.Factory().Process()
			result := expectProjectLookup(t, processMock)
			test.lookup(result)

			got, err := resolveProject(context.Background(), paseoBin, "tmp")

			if test.wantErr != "" {
				require.ErrorContains(t, err, test.wantErr)
				require.Equal(t, Project{}, got)
			} else {
				require.NoError(t, err)
				require.Equal(t, test.want, got)
			}

			processMock.AssertExpectations(t)
			result.AssertExpectations(t)
		})
	}
}

func TestResolveProjectRejectsBlankName(t *testing.T) {
	factory := mock.Factory()

	_, err := resolveProject(context.Background(), paseoBin, "")

	require.EqualError(t, err, "agent: AGENT_PROJECT_NAME must name a paseo project")
	// A blank name is answered without paying for a subprocess.
	factory.App().AssertNotCalled(t, "MakeProcess")
}

func TestProjectResolverCachesSuccess(t *testing.T) {
	processMock := mock.Factory().Process()
	result := expectProjectLookup(t, processMock)
	result.EXPECT().Failed().Return(false).Once()
	result.EXPECT().Output().
		Return(`[{"projectId":"prj_tmp","name":"tmp","path":"/tmp"}]`).
		Once()

	resolver := newProjectResolver(paseoBin, "tmp")

	first, err := resolver.Project(context.Background())
	require.NoError(t, err)
	require.Equal(t, Project{ID: "prj_tmp", Path: "/tmp"}, first)

	// The second call must reuse the answer: the single `.Once()` lookup above
	// fails the strict mock if it runs again.
	second, err := resolver.Project(context.Background())
	require.NoError(t, err)
	require.Equal(t, first, second)

	processMock.AssertExpectations(t)
	result.AssertExpectations(t)
}

func TestProjectResolverDoesNotCacheFailure(t *testing.T) {
	processMock := mock.Factory().Process()

	failed := expectProjectLookup(t, processMock)
	failed.EXPECT().Failed().Return(true).Once()
	failed.EXPECT().ExitCode().Return(1).Once()
	failed.EXPECT().ErrorOutput().Return("daemon down").Once()

	succeeded := expectProjectLookup(t, processMock)
	succeeded.EXPECT().Failed().Return(false).Once()
	succeeded.EXPECT().Output().
		Return(`[{"projectId":"prj_tmp","name":"tmp","path":"/tmp"}]`).
		Once()

	resolver := newProjectResolver(paseoBin, "tmp")

	_, err := resolver.Project(context.Background())
	var projectErr projectError
	require.ErrorAs(t, err, &projectErr)

	// A failed lookup is retried on the next mention.
	project, err := resolver.Project(context.Background())
	require.NoError(t, err)
	require.Equal(t, Project{ID: "prj_tmp", Path: "/tmp"}, project)

	processMock.AssertExpectations(t)
}

func TestSharedProjectResolverIsReused(t *testing.T) {
	first := sharedProjectResolver(paseoBin, "shared-test")
	second := sharedProjectResolver(paseoBin, "shared-test")

	require.Same(t, first, second)

	// Another name gets its own resolver, so one project's cache cannot answer
	// a lookup for a different one.
	require.NotSame(t, first, sharedProjectResolver(paseoBin, "shared-test-other"))
}

func TestResolvedProjectSkipsLookup(t *testing.T) {
	factory := mock.Factory()

	project, err := resolvedProject(Project{ID: "prj_tmp", Path: "/tmp/ws"}).Project(context.Background())

	require.NoError(t, err)
	require.Equal(t, Project{ID: "prj_tmp", Path: "/tmp/ws"}, project)
	factory.App().AssertNotCalled(t, "MakeProcess")
}
