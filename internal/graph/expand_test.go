package graph

import (
	"context"
	"fmt"
	"go/ast"
	"go/printer"
	"go/token"
	"os"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/testing/protocmp"

	"github.com/authzed/spicedb/internal/datastore/memdb"
	"github.com/authzed/spicedb/internal/namespace"
	"github.com/authzed/spicedb/internal/testfixtures"
	"github.com/authzed/spicedb/pkg/graph"
	pb "github.com/authzed/spicedb/pkg/proto/REDACTEDapi/api"
	"github.com/authzed/spicedb/pkg/tuple"
)

var (
	_this *pb.ObjectAndRelation

	companyOwner = graph.Leaf(ONR("folder", "company", "owner"),
		tuple.User(ONR("user", "owner", Ellipsis)),
	)
	companyEditor = graph.Union(ONR("folder", "company", "editor"),
		graph.Leaf(_this),
		companyOwner,
	)

	companyViewer = graph.Union(ONR("folder", "company", "viewer"),
		graph.Leaf(_this,
			tuple.User(ONR("user", "legal", "...")),
			tuple.User(ONR("folder", "auditors", "viewer")),
		),
		companyEditor,
		graph.Union(ONR("folder", "company", "viewer")),
	)

	auditorsOwner = graph.Leaf(ONR("folder", "auditors", "owner"))

	auditorsEditor = graph.Union(ONR("folder", "auditors", "editor"),
		graph.Leaf(_this),
		auditorsOwner,
	)

	auditorsViewerRecursive = graph.Union(ONR("folder", "auditors", "viewer"),
		graph.Leaf(_this,
			tuple.User(ONR("user", "auditor", "...")),
		),
		auditorsEditor,
		graph.Union(ONR("folder", "auditors", "viewer")),
	)

	companyViewerRecursive = graph.Union(ONR("folder", "company", "viewer"),
		graph.Union(ONR("folder", "company", "viewer"),
			auditorsViewerRecursive,
			graph.Leaf(_this,
				tuple.User(ONR("user", "legal", "...")),
				tuple.User(ONR("folder", "auditors", "viewer")),
			),
		),
		companyEditor,
		graph.Union(ONR("folder", "company", "viewer")),
	)

	docOwner = graph.Leaf(ONR("document", "masterplan", "owner"),
		tuple.User(ONR("user", "product_manager", "...")),
	)
	docEditor = graph.Union(ONR("document", "masterplan", "editor"),
		graph.Leaf(_this),
		docOwner,
	)
	docViewer = graph.Union(ONR("document", "masterplan", "viewer"),
		graph.Leaf(_this,
			tuple.User(ONR("user", "eng_lead", "...")),
		),
		docEditor,
		graph.Union(ONR("document", "masterplan", "viewer"),
			graph.Union(ONR("folder", "plans", "viewer"),
				graph.Leaf(_this,
					tuple.User(ONR("user", "chief_financial_officer", "...")),
				),
				graph.Union(ONR("folder", "plans", "editor"),
					graph.Leaf(_this),
					graph.Leaf(ONR("folder", "plans", "owner")),
				),
				graph.Union(ONR("folder", "plans", "viewer")),
			),
			graph.Union(ONR("folder", "strategy", "viewer"),
				graph.Leaf(_this),
				graph.Union(ONR("folder", "strategy", "editor"),
					graph.Leaf(_this),
					graph.Leaf(ONR("folder", "strategy", "owner"),
						tuple.User(ONR("user", "vp_product", "...")),
					),
				),
				graph.Union(ONR("folder", "strategy", "viewer"),
					companyViewer,
				),
			),
		),
	)
)

func TestExpand(t *testing.T) {
	testCases := []struct {
		start         *pb.ObjectAndRelation
		expansionMode ExpansionMode
		expected      *pb.RelationTupleTreeNode
	}{
		{start: ONR("folder", "company", "owner"), expansionMode: ShallowExpansion, expected: companyOwner},
		{start: ONR("folder", "company", "editor"), expansionMode: ShallowExpansion, expected: companyEditor},
		{start: ONR("folder", "company", "viewer"), expansionMode: ShallowExpansion, expected: companyViewer},
		{start: ONR("document", "masterplan", "owner"), expansionMode: ShallowExpansion, expected: docOwner},
		{start: ONR("document", "masterplan", "editor"), expansionMode: ShallowExpansion, expected: docEditor},
		{start: ONR("document", "masterplan", "viewer"), expansionMode: ShallowExpansion, expected: docViewer},

		{start: ONR("folder", "auditors", "owner"), expansionMode: RecursiveExpansion, expected: auditorsOwner},
		{start: ONR("folder", "auditors", "editor"), expansionMode: RecursiveExpansion, expected: auditorsEditor},
		{start: ONR("folder", "auditors", "viewer"), expansionMode: RecursiveExpansion, expected: auditorsViewerRecursive},

		{start: ONR("folder", "company", "owner"), expansionMode: RecursiveExpansion, expected: companyOwner},
		{start: ONR("folder", "company", "editor"), expansionMode: RecursiveExpansion, expected: companyEditor},
		{start: ONR("folder", "company", "viewer"), expansionMode: RecursiveExpansion, expected: companyViewerRecursive},
	}

	for _, tc := range testCases {
		t.Run(fmt.Sprintf("%s-%v", tuple.StringONR(tc.start), tc.expansionMode), func(t *testing.T) {
			require := require.New(t)

			dispatch, revision := newLocalDispatcher(require)

			expandResult := dispatch.Expand(context.Background(), ExpandRequest{
				Start:          tc.start,
				AtRevision:     revision,
				DepthRemaining: 50,
				ExpansionMode:  tc.expansionMode,
			})

			require.NoError(expandResult.Err)
			require.NotNil(expandResult.Tree)

			if diff := cmp.Diff(tc.expected, expandResult.Tree, protocmp.Transform()); diff != "" {
				fset := token.NewFileSet()
				err := printer.Fprint(os.Stdout, fset, serializeToFile(expandResult.Tree))
				require.NoError(err)
				t.Errorf("unexpected difference:\n%v", diff)
			}
		})
	}
}

func serializeToFile(node *pb.RelationTupleTreeNode) *ast.File {
	return &ast.File{
		Package: 1,
		Name: &ast.Ident{
			Name: "main",
		},
		Decls: []ast.Decl{
			&ast.FuncDecl{
				Name: &ast.Ident{
					Name: "main",
				},
				Type: &ast.FuncType{
					Params: &ast.FieldList{},
				},
				Body: &ast.BlockStmt{
					List: []ast.Stmt{
						&ast.ExprStmt{
							X: serialize(node),
						},
					},
				},
			},
		},
	}
}

func serialize(node *pb.RelationTupleTreeNode) *ast.CallExpr {
	var expanded ast.Expr = ast.NewIdent("_this")
	if node.Expanded != nil {
		expanded = onrExpr(node.Expanded)
	}

	children := []ast.Expr{expanded}

	var fName string
	switch node.NodeType.(type) {
	case *pb.RelationTupleTreeNode_IntermediateNode:
		switch node.GetIntermediateNode().Operation {
		case pb.SetOperationUserset_EXCLUSION:
			fName = "tf.E"
		case pb.SetOperationUserset_INTERSECTION:
			fName = "tf.I"
		case pb.SetOperationUserset_UNION:
			fName = "tf.U"
		default:
			panic("Unknown set operation")
		}

		for _, child := range node.GetIntermediateNode().ChildNodes {
			children = append(children, serialize(child))
		}

	case *pb.RelationTupleTreeNode_LeafNode:
		fName = "tf.Leaf"
		for _, user := range node.GetLeafNode().Users {
			onrExpr := onrExpr(user.GetUserset())
			children = append(children, &ast.CallExpr{
				Fun:  ast.NewIdent("User"),
				Args: []ast.Expr{onrExpr},
			})
		}
	}

	return &ast.CallExpr{
		Fun:  ast.NewIdent(fName),
		Args: children,
	}
}

func onrExpr(onr *pb.ObjectAndRelation) ast.Expr {
	return &ast.CallExpr{
		Fun: ast.NewIdent("tf.ONR"),
		Args: []ast.Expr{
			ast.NewIdent("\"" + onr.Namespace + "\""),
			ast.NewIdent("\"" + onr.ObjectId + "\""),
			ast.NewIdent("\"" + onr.Relation + "\""),
		},
	}
}

func TestMaxDepthExpand(t *testing.T) {
	require := require.New(t)

	rawDS, err := memdb.NewMemdbDatastore(0, 0, memdb.DisableGC, 0)
	require.NoError(err)

	ds, _ := testfixtures.StandardDatastoreWithSchema(rawDS, require)

	mutations := []*pb.RelationTupleUpdate{
		tuple.Create(&pb.RelationTuple{
			ObjectAndRelation: ONR("folder", "oops", "parent"),
			User:              tuple.User(ONR("folder", "oops", Ellipsis)),
		}),
	}

	ctx := context.Background()

	revision, err := ds.WriteTuples(ctx, nil, mutations)
	require.NoError(err)
	require.True(revision.GreaterThan(decimal.Zero))

	nsm, err := namespace.NewCachingNamespaceManager(ds, 1*time.Second, testCacheConfig)
	require.NoError(err)

	dispatch, err := NewLocalDispatcher(nsm, ds)
	require.NoError(err)

	checkResult := dispatch.Expand(ctx, ExpandRequest{
		Start:          ONR("folder", "oops", "viewer"),
		AtRevision:     revision,
		DepthRemaining: 50,
		ExpansionMode:  ShallowExpansion,
	})

	require.Error(checkResult.Err)
}
