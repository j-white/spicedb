package compiler

import (
	"fmt"

	"github.com/jzelinskie/stringz"

	"github.com/authzed/spicedb/pkg/namespace"
	pb "github.com/authzed/spicedb/pkg/proto/REDACTEDapi/api"
	"github.com/authzed/spicedb/pkg/schemadsl/dslshape"
)

type translationContext struct {
	objectTypePrefix string
}

func (tc translationContext) NamespacePath(namespaceName string) string {
	return tc.objectTypePrefix + "/" + namespaceName
}

const Ellipsis = "..."

func translate(tctx translationContext, root *dslNode) ([]*pb.NamespaceDefinition, error) {
	definitions := []*pb.NamespaceDefinition{}
	for _, definitionNode := range root.GetChildren() {
		definition, err := translateDefinition(tctx, definitionNode)
		if err != nil {
			return []*pb.NamespaceDefinition{}, err
		}

		definitions = append(definitions, definition)
	}

	return definitions, nil
}

func translateDefinition(tctx translationContext, defNode *dslNode) (*pb.NamespaceDefinition, error) {
	definitionName, err := defNode.GetString(dslshape.NodeDefinitionPredicateName)
	if err != nil {
		return nil, defNode.Errorf("invalid definition name: %w", err)
	}

	relationsAndPermissions := []*pb.Relation{}
	for _, relationOrPermissionNode := range defNode.GetChildren() {
		relationOrPermission, err := translateRelationOrPermission(tctx, relationOrPermissionNode)
		if err != nil {
			return nil, err
		}

		relationsAndPermissions = append(relationsAndPermissions, relationOrPermission)
	}

	if len(relationsAndPermissions) == 0 {
		return namespace.Namespace(tctx.NamespacePath(definitionName)), nil
	}

	return namespace.Namespace(tctx.NamespacePath(definitionName), relationsAndPermissions...), nil
}

func translateRelationOrPermission(tctx translationContext, relOrPermNode *dslNode) (*pb.Relation, error) {
	switch relOrPermNode.GetType() {
	case dslshape.NodeTypeRelation:
		return translateRelation(tctx, relOrPermNode)

	case dslshape.NodeTypePermission:
		return translatePermission(tctx, relOrPermNode)

	default:
		return nil, relOrPermNode.Errorf("unknown definition top-level node type %s", relOrPermNode.GetType())
	}
}

func translateRelation(tctx translationContext, relationNode *dslNode) (*pb.Relation, error) {
	relationName, err := relationNode.GetString(dslshape.NodePredicateName)
	if err != nil {
		return nil, relationNode.Errorf("invalid relation name: %w", err)
	}

	allowedDirectTypes := []*pb.RelationReference{}
	for _, typeRef := range relationNode.List(dslshape.NodeRelationPredicateAllowedTypes) {
		relReferences, err := translateTypeReference(tctx, typeRef)
		if err != nil {
			return nil, err
		}

		allowedDirectTypes = append(allowedDirectTypes, relReferences...)
	}

	return namespace.Relation(relationName, nil, allowedDirectTypes...), nil
}

func translatePermission(tctx translationContext, permissionNode *dslNode) (*pb.Relation, error) {
	permissionName, err := permissionNode.GetString(dslshape.NodePredicateName)
	if err != nil {
		return nil, permissionNode.Errorf("invalid permission name: %w", err)
	}

	expressionNode, err := permissionNode.Lookup(dslshape.NodePermissionPredicateComputeExpression)
	if err != nil {
		return nil, permissionNode.Errorf("invalid permission expression: %w", err)
	}

	rewrite, err := translateExpression(tctx, expressionNode)
	if err != nil {
		return nil, err
	}

	return namespace.Relation(permissionName, rewrite), nil
}

func translateBinary(tctx translationContext, expressionNode *dslNode) (*pb.SetOperation_Child, *pb.SetOperation_Child, error) {
	leftChild, err := expressionNode.Lookup(dslshape.NodeExpressionPredicateLeftExpr)
	if err != nil {
		return nil, nil, err
	}

	rightChild, err := expressionNode.Lookup(dslshape.NodeExpressionPredicateRightExpr)
	if err != nil {
		return nil, nil, err
	}

	leftOperation, err := translateExpressionOperation(tctx, leftChild)
	if err != nil {
		return nil, nil, err
	}

	rightOperation, err := translateExpressionOperation(tctx, rightChild)
	if err != nil {
		return nil, nil, err
	}

	return leftOperation, rightOperation, nil
}

func translateExpression(tctx translationContext, expressionNode *dslNode) (*pb.UsersetRewrite, error) {
	switch expressionNode.GetType() {
	case dslshape.NodeTypeUnionExpression:
		leftOperation, rightOperation, err := translateBinary(tctx, expressionNode)
		if err != nil {
			return nil, err
		}
		return namespace.Union(leftOperation, rightOperation), nil

	case dslshape.NodeTypeIntersectExpression:
		leftOperation, rightOperation, err := translateBinary(tctx, expressionNode)
		if err != nil {
			return nil, err
		}
		return namespace.Intersection(leftOperation, rightOperation), nil

	case dslshape.NodeTypeExclusionExpression:
		leftOperation, rightOperation, err := translateBinary(tctx, expressionNode)
		if err != nil {
			return nil, err
		}
		return namespace.Exclusion(leftOperation, rightOperation), nil

	default:
		op, err := translateExpressionOperation(tctx, expressionNode)
		if err != nil {
			return nil, err
		}

		return namespace.Union(op), nil
	}
}

func translateExpressionOperation(tctx translationContext, expressionOpNode *dslNode) (*pb.SetOperation_Child, error) {
	switch expressionOpNode.GetType() {
	case dslshape.NodeTypeIdentifier:
		referencedRelationName, err := expressionOpNode.GetString(dslshape.NodeIdentiferPredicateValue)
		if err != nil {
			return nil, err
		}

		return namespace.ComputedUserset(referencedRelationName), nil

	case dslshape.NodeTypeArrowExpression:
		leftChild, err := expressionOpNode.Lookup(dslshape.NodeExpressionPredicateLeftExpr)
		if err != nil {
			return nil, err
		}

		rightChild, err := expressionOpNode.Lookup(dslshape.NodeExpressionPredicateRightExpr)
		if err != nil {
			return nil, err
		}

		if leftChild.GetType() != dslshape.NodeTypeIdentifier {
			return nil, leftChild.Errorf("Nested arrows not yet supported")
		}

		tuplesetRelation, err := leftChild.GetString(dslshape.NodeIdentiferPredicateValue)
		if err != nil {
			return nil, err
		}

		usersetRelation, err := rightChild.GetString(dslshape.NodeIdentiferPredicateValue)
		if err != nil {
			return nil, err
		}

		return namespace.TupleToUserset(tuplesetRelation, usersetRelation), nil

	case dslshape.NodeTypeUnionExpression:
		fallthrough

	case dslshape.NodeTypeIntersectExpression:
		fallthrough

	case dslshape.NodeTypeExclusionExpression:
		rewrite, err := translateExpression(tctx, expressionOpNode)
		if err != nil {
			return nil, err
		}
		return namespace.Rewrite(rewrite), nil

	default:
		return nil, expressionOpNode.Errorf("unknown expression node type %s", expressionOpNode.GetType())
	}
}

func translateTypeReference(tctx translationContext, typeRefNode *dslNode) ([]*pb.RelationReference, error) {
	switch typeRefNode.GetType() {
	case dslshape.NodeTypeTypeReference:
		references := []*pb.RelationReference{}
		for _, subRefNode := range typeRefNode.List(dslshape.NodeTypeReferencePredicateType) {
			subReferences, err := translateTypeReference(tctx, subRefNode)
			if err != nil {
				return []*pb.RelationReference{}, err
			}

			references = append(references, subReferences...)
		}
		return references, nil

	case dslshape.NodeTypeSpecificTypeReference:
		ref, err := translateSpecificTypeReference(tctx, typeRefNode)
		if err != nil {
			return []*pb.RelationReference{}, err
		}
		return []*pb.RelationReference{ref}, nil

	default:
		return nil, typeRefNode.Errorf("unknown type ref node type %s", typeRefNode.GetType())
	}
}

func translateSpecificTypeReference(tctx translationContext, typeRefNode *dslNode) (*pb.RelationReference, error) {
	typePath, err := typeRefNode.GetString(dslshape.NodeSpecificReferencePredicateType)
	if err != nil {
		return nil, typeRefNode.Errorf("invalid type name: %w", err)
	}

	var typePrefix, typeName string
	if err := stringz.SplitExact(typePath, "/", &typePrefix, &typeName); err != nil {
		typePrefix = tctx.objectTypePrefix
		typeName = typePath
	}

	relationName := Ellipsis
	if typeRefNode.Has(dslshape.NodeSpecificReferencePredicateRelation) {
		relationName, err = typeRefNode.GetString(dslshape.NodeSpecificReferencePredicateRelation)
		if err != nil {
			return nil, typeRefNode.Errorf("invalid type relation: %w", err)
		}
	}

	return &pb.RelationReference{
		Namespace: fmt.Sprintf("%s/%s", typePrefix, typeName),
		Relation:  relationName,
	}, nil
}
