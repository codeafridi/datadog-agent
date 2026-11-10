"""dd__pkg_deb - wrapper for pkg_deb adding agent specific defaults."""

load("@rules_pkg//pkg:tar.bzl", "pkg_tar")

# We need to peek at the actual rule instead of the wrapper because you
# can't put a macro on a legacy macro.
# buildifier: disable=bzl-visibility
load("@rules_pkg//pkg/private/deb:deb.bzl", "pkg_deb_impl")
load("//bazel/rules:compression.bzl", "get_compression_level")
load("//bazel/rules:xz_compress.bzl", "xz_compress")
load("//bazel/rules/dd_strip:get_debug_symbols.bzl", "get_debug_symbols")
load("//bazel/tools/tar_checksums:tar_md5sums.bzl", "tar_md5sums")
load("//packages/rules:description.bzl", "deb_description")
load("//packages/rules:package_naming.bzl", "package_name_variables")

def _deb_package(name, srcs, variables, **kwargs):
    data_tar = "%s_data_tar" % name
    pkg_tar(
        name = data_tar,
        srcs = srcs,
        out = "%s_data.tar" % name,
        owner = "0.0",
        tags = ["manual"],
        visibility = ["//visibility:private"],
    )

    data = "%s_data" % name
    xz_compress(
        name = data,
        src = ":" + data_tar,
        out = "%s_data.tar.xz" % name,
        # TODO: account for pipeline injected compression level.
        # https://datadoghq.atlassian.net/browse/ABLD-364
        level = get_compression_level(),
        tags = ["manual"],
        visibility = ["//visibility:private"],
    )

    sums_out = "%s_md5sums_out_" % name
    size_out = "%s_installed_size_out_" % name
    tar_md5sums(
        name = "%s_md5sums_" % name,
        src = ":" + data_tar,
        installed_size = size_out,
        md5sums = sums_out,
    )

    pkg_deb_impl(
        name = name,
        data = ":" + data,
        installed_size_file = ":" + size_out,
        md5sums = ":" + sums_out,
        package_variables = ":" + variables,
        **kwargs
    )

# kwargs is mandatory for macros, even if you don't use it.
# buildifier: disable=unused-variable
def _dd_pkg_deb_impl(name, visibility, architecture, conflicts, debug_package, depends, description, homepage, license, maintainer, out, package_file_name, postinst, postrm, preinst, prerm, priority, product_name, recommends, section, srcs, version, **kwargs):
    variables_name = "%s_vars_" % name
    package_name_variables(
        name = variables_name,
        product_name = product_name,
    )

    common = dict(
        architecture = architecture or "{arch_deb}",
        conflicts = conflicts,
        description = deb_description(description),
        homepage = homepage or "http://www.datadoghq.com",
        license = license or "Apache License Version 2.0",
        maintainer = maintainer or "Datadog Packages <package@datadoghq.com>",
        priority = priority or "extra",
        recommends = recommends,
        section = section or "utils",
        version = version,
        visibility = visibility,
    )

    _deb_package(
        name = name,
        srcs = srcs,
        variables = variables_name,
        depends = depends,
        out = out,
        package = "{product_name}",
        package_file_name = package_file_name,
        postinst = postinst,
        postrm = postrm,
        preinst = preinst,
        prerm = prerm,
        **common
    )

    if not debug_package:
        return

    if not package_file_name.startswith("{product_name}"):
        fail("debug_package requires package_file_name to start with {product_name}, got: " + package_file_name)

    debug_symbols = "%s_debug_symbols" % name
    get_debug_symbols(
        name = debug_symbols,
        srcs = srcs,
        prefix = "{install_dir}/.debug",
        tags = ["manual"],
        visibility = ["//visibility:private"],
    )

    _deb_package(
        name = "%s_dbg" % name,
        srcs = [":" + debug_symbols],
        variables = variables_name,
        depends = ["{product_name} (= %s)" % version],
        out = "%s_dbg.deb" % name,
        package = "{product_name}-dbg",
        package_file_name = "{product_name}-dbg" + package_file_name.removeprefix("{product_name}"),
        **common
    )

dd_pkg_deb = macro(
    doc = """Build a debian package.

With debug_package, also build <name>_dbg: a {product_name}-dbg package holding
the debug symbols of the binaries installed by srcs.
""",
    inherit_attrs = pkg_deb_impl,
    implementation = _dd_pkg_deb_impl,
    attrs = {
        "data": None,
        "debug_package": attr.bool(
            doc = "Also build a <name>_dbg debug symbols package.",
            default = False,
            configurable = False,
        ),
        "description": attr.string(configurable = False),
        "installed_size": None,
        "installed_size_file": None,
        "md5sums": None,
        "package": None,
        "package_file_name": attr.string(configurable = False),
        "package_variables": None,
        "product_name": attr.string(
            doc = "Unflavored product name. The flavor is added to it to form the package name.",
            mandatory = True,
        ),
        "srcs": attr.label_list(
            doc = "Files to install, compressed with xz into the package's data archive.",
            mandatory = True,
        ),
        "version": attr.string(configurable = False),
    },
)
