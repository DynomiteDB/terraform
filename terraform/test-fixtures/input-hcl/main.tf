variable "mapped" {
    type = "map"
}

variable "listed" {
    type = "list"
}

resource "hcl_instance" "foo" {
    foo = "${var.listed}"
    bar = "${var.mapped}"
}
