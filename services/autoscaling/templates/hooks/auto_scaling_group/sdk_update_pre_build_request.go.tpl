updatedDesired := desired.DeepCopy()
updatedDesired.SetStatus(latest)
if delta.DifferentAt("Spec.Tags") {
    name := string(*latest.ko.Spec.Name)
    err = syncTags(
        ctx, 
        desired.ko.Spec.Tags, latest.ko.Spec.Tags, 
        &name, convertToOrderedACKTags, rm.sdkapi, rm.metrics,
    )
    if err != nil {
        return nil, err
    }
}
if delta.DifferentAt("Spec.EnabledMetrics") {
    name := string(*latest.ko.Spec.Name)
    if err := syncEnabledMetrics(ctx, desired.ko.Spec.EnabledMetrics, latest.ko.Spec.EnabledMetrics, name, rm.sdkapi, rm.metrics); err != nil {
        return nil, err
    }
}
if delta.DifferentAt("Spec.SuspendedProcesses") {
    name := string(*latest.ko.Spec.Name)
    if err := syncSuspendedProcesses(ctx, desired.ko.Spec.SuspendedProcesses, latest.ko.Spec.SuspendedProcesses, name, rm.sdkapi, rm.metrics); err != nil {
        return nil, err
    }
}
if !delta.DifferentExcept("Spec.Tags", "Spec.EnabledMetrics", "Spec.SuspendedProcesses") {
    return rm.concreteResource(updatedDesired), nil
}
