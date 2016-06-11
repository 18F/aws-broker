module "rds_shared_mysql" {
    source = "git::https://github.com/18F/cg-provision//terraform/modules/rds"
    stack_description = "${var.stack_description}"
    rds_subnet_group = "${terraform_remote_state.vpc.output.rds_subnet_group}"
    /* TODO: Use database instance type from config */
    rds_security_groups = "${terraform_remote_state.vpc.output.rds_mysql_security_group}"

    rds_instance_type = "${var.rds_shared_mysql_instance_type}"
    rds_db_size = "${var.rds_shared_mysql_db_size}"
    rds_db_name = "${var.rds_shared_mysql_db_name}"
    rds_db_engine = "${var.rds_shared_mysql_db_engine}"
    rds_db_engine_version = "${var.rds_shared_mysql_db_engine_version}"
    rds_username = "${var.rds_shared_mysql_username}"
    rds_password = "${var.rds_shared_mysql_password}"
}
