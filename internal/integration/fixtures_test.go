//go:build integration

package integration

// fixtureDDL creates a schema exercising every feature the export/import
// tool is supposed to capture: identity columns, primary/unique/check
// constraints, a non-key index with an INCLUDE column, a persisted computed
// column, a normal FK, a self-referencing FK, a two-table FK cycle, a wide
// spread of data types, and a rowversion column (which must never be
// targeted by an INSERT).
func fixtureDDL() []string {
	return []string{
		`CREATE TABLE dbo.Customers (
			CustomerID int IDENTITY(1,1) NOT NULL,
			Name nvarchar(100) NOT NULL,
			Email varchar(200) NOT NULL,
			Notes nvarchar(max) NULL,
			CreatedAt datetime2(3) NOT NULL CONSTRAINT DF_Customers_CreatedAt DEFAULT (SYSUTCDATETIME()),
			Age int NULL CONSTRAINT CK_Customers_Age CHECK (Age IS NULL OR Age >= 0),
			CONSTRAINT PK_Customers PRIMARY KEY CLUSTERED (CustomerID),
			CONSTRAINT UQ_Customers_Email UNIQUE (Email)
		)`,
		`CREATE INDEX IX_Customers_Name ON dbo.Customers (Name) INCLUDE (Email)`,

		`CREATE TABLE dbo.Orders (
			OrderID int IDENTITY(100,1) NOT NULL,
			CustomerID int NOT NULL,
			Amount decimal(10,2) NOT NULL,
			TotalWithTax AS (Amount * 1.1) PERSISTED,
			OrderDate date NOT NULL,
			IsPaid bit NOT NULL CONSTRAINT DF_Orders_IsPaid DEFAULT (0),
			CONSTRAINT PK_Orders PRIMARY KEY (OrderID)
		)`,
		`ALTER TABLE dbo.Orders ADD CONSTRAINT FK_Orders_Customers
			FOREIGN KEY (CustomerID) REFERENCES dbo.Customers(CustomerID) ON DELETE CASCADE`,

		`CREATE TABLE dbo.Employees (
			EmployeeID int IDENTITY(1,1) NOT NULL,
			FullName nvarchar(100) NOT NULL,
			ManagerID int NULL,
			CONSTRAINT PK_Employees PRIMARY KEY (EmployeeID)
		)`,
		`ALTER TABLE dbo.Employees ADD CONSTRAINT FK_Employees_Manager
			FOREIGN KEY (ManagerID) REFERENCES dbo.Employees(EmployeeID)`,

		`CREATE TABLE dbo.CycleA (
			AID int IDENTITY(1,1) NOT NULL,
			BID int NULL,
			Label varchar(20) NOT NULL,
			CONSTRAINT PK_CycleA PRIMARY KEY (AID)
		)`,
		`CREATE TABLE dbo.CycleB (
			BID int IDENTITY(1,1) NOT NULL,
			AID int NULL,
			Label varchar(20) NOT NULL,
			CONSTRAINT PK_CycleB PRIMARY KEY (BID)
		)`,
		`ALTER TABLE dbo.CycleA ADD CONSTRAINT FK_CycleA_CycleB FOREIGN KEY (BID) REFERENCES dbo.CycleB(BID)`,
		`ALTER TABLE dbo.CycleB ADD CONSTRAINT FK_CycleB_CycleA FOREIGN KEY (AID) REFERENCES dbo.CycleA(AID)`,

		`CREATE TABLE dbo.AllTypes (
			ID int IDENTITY(1,1) NOT NULL PRIMARY KEY,
			TinyIntCol tinyint NULL,
			SmallIntCol smallint NULL,
			IntCol int NULL,
			BigIntCol bigint NULL,
			BitCol bit NULL,
			DecimalCol decimal(18,4) NULL,
			NumericCol numeric(9,2) NULL,
			MoneyCol money NULL,
			SmallMoneyCol smallmoney NULL,
			FloatCol float NULL,
			RealCol real NULL,
			CharCol char(10) NULL,
			VarcharCol varchar(50) NULL,
			VarcharMaxCol varchar(max) NULL,
			NCharCol nchar(5) NULL,
			NVarcharCol nvarchar(50) NULL,
			NVarcharMaxCol nvarchar(max) NULL,
			DateCol date NULL,
			TimeCol time(7) NULL,
			DateTimeCol datetime NULL,
			DateTime2Col datetime2(7) NULL,
			SmallDateTimeCol smalldatetime NULL,
			DateTimeOffsetCol datetimeoffset(7) NULL,
			BinaryCol binary(4) NULL,
			VarbinaryCol varbinary(50) NULL,
			VarbinaryMaxCol varbinary(max) NULL,
			GuidCol uniqueidentifier NULL
		)`,

		`CREATE TABLE dbo.LegacyTypes (
			ID int IDENTITY(1,1) NOT NULL PRIMARY KEY,
			XmlCol xml NULL,
			TextCol text NULL,
			NTextCol ntext NULL,
			RowVer rowversion NOT NULL
		)`,
	}
}

func fixtureData() []string {
	return []string{
		`INSERT INTO dbo.Customers (Name, Email, Notes, Age) VALUES
			(N'Alice', 'alice@example.com', N'VIP customer', 30),
			(N'Bob', 'bob@example.com', NULL, NULL),
			(N'O''Brien', 'obrien@example.com', N'has a quote '' in notes', 45)`,

		`INSERT INTO dbo.Orders (CustomerID, Amount, OrderDate, IsPaid) VALUES
			(1, 100.50, '2024-01-15', 1),
			(1, 250.00, '2024-02-20', 0),
			(2, 75.25, '2024-03-01', 1)`,

		`INSERT INTO dbo.Employees (FullName, ManagerID) VALUES (N'Carol CEO', NULL)`,
		`INSERT INTO dbo.Employees (FullName, ManagerID) VALUES (N'Dave Manager', 1)`,
		`INSERT INTO dbo.Employees (FullName, ManagerID) VALUES (N'Erin IC', 2)`,

		`INSERT INTO dbo.CycleA (BID, Label) VALUES (NULL, 'A1')`,
		`INSERT INTO dbo.CycleB (AID, Label) VALUES (NULL, 'B1')`,
		`UPDATE dbo.CycleA SET BID = (SELECT BID FROM dbo.CycleB WHERE Label = 'B1') WHERE Label = 'A1'`,
		`UPDATE dbo.CycleB SET AID = (SELECT AID FROM dbo.CycleA WHERE Label = 'A1') WHERE Label = 'B1'`,

		`INSERT INTO dbo.AllTypes DEFAULT VALUES`,
		`INSERT INTO dbo.AllTypes (
			TinyIntCol, SmallIntCol, IntCol, BigIntCol, BitCol, DecimalCol, NumericCol, MoneyCol, SmallMoneyCol,
			FloatCol, RealCol, CharCol, VarcharCol, VarcharMaxCol, NCharCol, NVarcharCol, NVarcharMaxCol,
			DateCol, TimeCol, DateTimeCol, DateTime2Col, SmallDateTimeCol, DateTimeOffsetCol,
			BinaryCol, VarbinaryCol, VarbinaryMaxCol, GuidCol
		) VALUES (
			255, -32768, 2147483647, 9223372036854775807, 1, 12345.6789, 1234.56, 19999.99, 214.74,
			3.14159265, 2.5, 'abc', 'O''Brien''s "quoted"', REPLICATE('x', 5000), N'héllo', N'wörld''s', N'unicode: 世界',
			'2024-03-05', '13:45:30.1234567', '2024-03-05T12:00:00', '2024-03-05T13:45:30.1234567', '2024-03-05T12:00:00',
			'2024-03-05T13:45:30.1234567+05:30',
			0x01020304, 0x0102FF00, 0xDEADBEEFCAFE, 'AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE'
		)`,

		`INSERT INTO dbo.LegacyTypes (XmlCol, TextCol, NTextCol) VALUES
			('<a><b>1</b></a>', 'legacy text value', N'legacy ntext value')`,
	}
}

// fixtureTables lists every table created by fixtureDDL, bracket-quoted, for
// row-count comparisons.
func fixtureTables() []string {
	return []string{
		"[dbo].[Customers]", "[dbo].[Orders]", "[dbo].[Employees]",
		"[dbo].[CycleA]", "[dbo].[CycleB]", "[dbo].[AllTypes]", "[dbo].[LegacyTypes]",
	}
}
