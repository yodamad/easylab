#!/bin/bash
# Test environment variables for EasyLab
# This file contains test-specific configuration

# Authentication passwords for testing
export LAB_ADMIN_PASSWORD=testpassword
export LAB_STUDENT_PASSWORD=studentpass

# Test directories (relative to project root)
export WORK_DIR=test-results/work
export DATA_DIR=test-results/data

# OVH credentials (can be overridden if needed for specific tests)
# These are optional for most tests
export OVH_APPLICATION_KEY=aa
export OVH_APPLICATION_SECRET=aa
export OVH_CONSUMER_KEY=aa
export OVH_SERVICE_NAME=aa
export OVH_ENDPOINT=ovh-eu

# Go workspace setting
export GOWORK=off

