package common

const (
	DescriptorAnnotationBucket       = "containerd.io/snapshot/bento-image-bucket"
	DescriptorAnnotationObjectKey    = "containerd.io/snapshot/bento-image-object-key"
	DescriptorAnnotationCompression  = "containerd.io/snapshot/bento-image-compression"
	DescriptorAnnotationBaseImage    = "containerd.io/snapshot/bento-image-base"
	DescriptorAnnotationIsBentoLayer = "containerd.io/snapshot/bento-image-is-bento-layer"
	DescriptorAnnotationFormat       = "containerd.io/snapshot/bento-image-format"

	DescriptorAnnotationValueFormatTar    = "tar"
	DescriptorAnnotationValueFormatStargz = "stargz"
)
