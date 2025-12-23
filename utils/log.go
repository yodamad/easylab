package utils

import "github.com/pulumi/pulumi/sdk/v3/go/pulumi"

func LogInfo(ctx *pulumi.Context, message string) {
	ctx.Log.Info(message, nil)
}

func LogError(ctx *pulumi.Context, message string) {
	ctx.Log.Error(message, nil)
}

func logWarning(ctx *pulumi.Context, message string) {
	ctx.Log.Warn(message, nil)
}

func logDebug(ctx *pulumi.Context, message string) {
	ctx.Log.Debug(message, nil)
}
