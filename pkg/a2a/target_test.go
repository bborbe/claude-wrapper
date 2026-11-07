// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package a2a_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/claude-wrapper/pkg/a2a"
)

var _ = Describe("ParseTarget", func() {
	ctx := context.Background()

	It("parses docker", func() {
		target, err := a2a.ParseTarget(ctx, "docker")
		Expect(err).NotTo(HaveOccurred())
		Expect(target.Kind).To(Equal(a2a.KindDocker))
		Expect(target.String()).To(Equal("docker"))
	})

	It("parses a bare k8s as the k8s kind with no cluster", func() {
		target, err := a2a.ParseTarget(ctx, "k8s")
		Expect(err).NotTo(HaveOccurred())
		Expect(target.Kind).To(Equal(a2a.KindK8s))
		Expect(target.Cluster).To(BeEmpty())
	})

	It("parses k8s:<cluster> and records the cluster", func() {
		target, err := a2a.ParseTarget(ctx, "k8s:nukedev")
		Expect(err).NotTo(HaveOccurred())
		Expect(target.Cluster).To(Equal("nukedev"))
		Expect(target.String()).To(Equal("k8s:nukedev"))
	})

	It("refuses an uppercase cluster rather than normalising it", func() {
		_, err := a2a.ParseTarget(ctx, "k8s:NukeDev")
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("refused rather than normalised"))
	})

	It("names the 2026-10-15 retirement for the retired cluster target", func() {
		_, err := a2a.ParseTarget(ctx, "cluster")
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("RETIRED on 2026-10-15"))
	})

	It("refuses an unknown target rather than defaulting to local", func() {
		_, err := a2a.ParseTarget(ctx, "pod")
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring(`expected "docker", "k8s"`))
	})
})

var _ = Describe("ClusterEnvSuffix", func() {
	It("uppercases and replaces hyphens", func() {
		Expect(a2a.ClusterEnvSuffix("octopus-dev")).To(Equal("OCTOPUS_DEV"))
		Expect(a2a.ClusterEnvSuffix("nukedev")).To(Equal("NUKEDEV"))
	})
})

var _ = Describe("RefuseSpawnArguments", func() {
	ctx := context.Background()
	interactive := true

	DescribeTable("refuses every local-only argument on an A2A target",
		func(args a2a.SpawnArguments, want string) {
			err := a2a.RefuseSpawnArguments(ctx, args)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring(want))
		},
		Entry("env", a2a.SpawnArguments{Target: "docker", Task: "t", Env: map[string]string{"A": "B"}}, "env is not supported"),
		Entry("resume", a2a.SpawnArguments{Target: "docker", Task: "t", Resume: true}, "resume is not supported"),
		Entry("policy", a2a.SpawnArguments{Target: "docker", Task: "t", Policy: "p"}, "policy is not supported"),
		Entry("interactive", a2a.SpawnArguments{Target: "docker", Task: "t", Interactive: &interactive}, "interactive is not supported"),
		Entry("window_id", a2a.SpawnArguments{Target: "docker", Task: "t", WindowID: "1"}, "window_id is not supported"),
		Entry("role", a2a.SpawnArguments{Target: "docker", Task: "t", Role: "worker"}, "role is not supported"),
		Entry("shipping", a2a.SpawnArguments{Target: "docker", Task: "t", Shipping: true}, "shipping is not supported"),
	)

	It("requires a task", func() {
		err := a2a.RefuseSpawnArguments(ctx, a2a.SpawnArguments{Target: "docker"})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("requires `task`"))
	})

	It("accepts a plain docker spawn", func() {
		Expect(a2a.RefuseSpawnArguments(ctx, a2a.SpawnArguments{
			Target: "docker", Task: "some task",
		})).To(Succeed())
	})
})
