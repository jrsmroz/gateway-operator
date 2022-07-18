---
title: Plugins
status: provisional
---

# Plugins

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
<!-- /toc -->

## Summary

The Gateway Operator provides lifecycle management for the Kong Gateway and
several relevant components (e.g. control planes such as the [Kong Kubernetes
ingress Controller (KIC)][kic]) but that management is not currently extensible.
Presently anyone who wanted to add additional functionality (in terms of new
API support, webhooks and controllers) would need to get that functionality
added directly to the Kong Gateway Operator respository upstream in order to
avoid forking the repository and building their own. Kong prides itself on
extensibility: The purpose of this KEP is to add a plugin system for this
operator which will allow additional management features to be added dynamically
by downstream users.

[kic]:https://github.com/kong/kubernetes-ingress-controller

## Motivation

- make it possible to add functionality "hooks" for existing functionality
- enable aftermarket features and API support that don't necessarily make sense
  to be available upstream to be added dynamically
- enable building an enterprise version of the operator without forking

### Goals

- make it possible to add go plugins that will add new API support
- make it possible to add go plugins that will add new webhook functionality
- make it possible to add go plugins that will load new controllers
- ensure our docker image can be used cleanly as a base layer, adding plugin
  files on top with minimal/no configuration in downstream builds
