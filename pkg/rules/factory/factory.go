package factory

import (
	"github.com/jenkins-x/jx-promote/pkg/rules"
	"github.com/jenkins-x/jx-promote/pkg/rules/apps"
	"github.com/jenkins-x/jx-promote/pkg/rules/file"
	"github.com/jenkins-x/jx-promote/pkg/rules/helm"
	"github.com/jenkins-x/jx-promote/pkg/rules/helmfile"
	"github.com/jenkins-x/jx-promote/pkg/rules/kpt"
)

// NewFunction creates a function based on the kind of rule
func NewFunction(r *rules.PromoteRule) rules.RuleFunction {
	spec := r.Config.Spec
	if spec.AppsRule != nil {
		return apps.AppsRule
	}
	if spec.FileRule != nil {
		return file.FileRule
	}
	if spec.HelmRule != nil {
		return helm.HelmRule
	}
	if spec.HelmfileRule != nil {
		return helmfile.HelmfileRule
	}
	if spec.KptRule != nil {
		return kpt.KptRule
	}
	return nil
}
