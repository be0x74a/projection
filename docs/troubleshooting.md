# Troubleshooting

One entry per failure-mode condition reason you may see on a `Projection`. Healthy reasons (`Resolved`, `Projected`) are not listed — when everything works there is nothing to troubleshoot.

Every entry assumes you have already located the failing condition. If you haven't, start at [observability.md](observability.md#reasons-youll-see) to learn how to read conditions and events, then come back via the reason link.

## Contents

**`SourceResolved` failures** — the controller could not locate or validate your source object:

- [SourceResolutionFailed](#sourceresolutionfailed)
- [SourceFetchFailed](#sourcefetchfailed)
- [SourceDeleted](#sourcedeleted)
- [SourceOptedOut / SourceNotProjectable](#sourceoptedout--sourcenotprojectable)

**`DestinationWritten` failures** — the controller located the source but could not write the destination:

- [SourceNotResolved](#sourcenotresolved) *(cascade from a `SourceResolved` failure)*
- [InvalidSpec](#invalidspec)
- [NamespaceResolutionFailed](#namespaceresolutionfailed)
- [DestinationFetchFailed](#destinationfetchfailed)
- [DestinationConflict](#destinationconflict)
- [DestinationCreateFailed](#destinationcreatefailed)
- [DestinationUpdateFailed](#destinationupdatefailed)
- [DestinationWriteFailed](#destinationwritefailed) *(rollup across multiple namespaces)*

## `SourceResolved` failures

<!-- entries filled in subsequent tasks -->

## `DestinationWritten` failures

<!-- entries filled in subsequent tasks -->
